//go:build whatsmeow

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types/events"

	"transactw/internal/conversation"
	"transactw/internal/inference"
	"transactw/internal/persistence"
)

const (
	receiptImageMediaType = "receipt_image"
	receiptImageMaxBytes  = 5_000_000
	receiptJobTimeout     = 45 * time.Second
)

func (g gateway) enqueueReceiptImage(ctx context.Context, evt *events.Message, image *waProto.ImageMessage, conversationKey string) bool {
	pending, err := g.db.PendingMediaJobCount(ctx, receiptImageMediaType)
	if err != nil {
		g.logger.Error("failed to count receipt jobs", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		return false
	}
	if g.cfg.MediaQueueMaxPending > 0 && pending >= g.cfg.MediaQueueMaxPending {
		return g.sendAndRecordReply(ctx, evt, conversationKey, "Lagi banyak struk yang diproses. Coba lagi nanti atau ketik transaksinya.", "receipt queue full reply")
	}

	imageData, err := g.client.Download(ctx, image)
	if err != nil {
		g.logger.Error("failed to download whatsmeow image", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		return g.sendAndRecordReply(ctx, evt, conversationKey, "Struk belum bisa diproses. Coba kirim ulang atau ketik transaksinya.", "receipt download failure reply")
	}
	if len(imageData) > receiptImageMaxBytes {
		return g.sendAndRecordReply(ctx, evt, conversationKey, "Foto struk terlalu besar. Coba kirim foto yang lebih kecil atau ketik transaksinya.", "receipt too large reply")
	}
	storagePath, imageHash, err := writeTempMedia(g.cfg.MediaTempDir, receiptImageMediaType, evt.Info.ID, imageData)
	if err != nil {
		g.logger.Error("failed to store receipt temp file", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		return false
	}

	job, duplicate, err := g.db.CreateMediaJob(ctx, persistence.CreateMediaJobInput{
		ConversationKey:   conversationKey,
		ProviderMessageID: evt.Info.ID,
		ChatID:            evt.Info.Chat.String(),
		MediaType:         receiptImageMediaType,
		MimeType:          image.GetMimetype(),
		StoragePath:       storagePath,
		MediaHash:         imageHash,
	})
	if err != nil {
		removeTempMedia(storagePath)
		g.logger.Error("failed to create receipt media job", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		return false
	}
	if duplicate {
		removeTempMedia(storagePath)
		message := "Struk ini sudah diterima."
		if job.Status == "queued" || job.Status == "processing" {
			message = "Struk ini masih diproses."
		}
		return g.sendAndRecordReply(ctx, evt, conversationKey, message, "duplicate receipt job reply")
	}
	g.logger.Info("queued receipt image", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "job_id", job.ID, "image_hash", imageHash)
	return g.sendAndRecordReply(ctx, evt, conversationKey, "Struk diterima, sedang diproses.", "receipt queued reply")
}

func (g gateway) startReceiptWorkers(ctx context.Context) {
	concurrency := g.cfg.ReceiptWorkerConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	for index := 0; index < concurrency; index++ {
		go g.receiptWorker(ctx, index+1)
	}
}

func (g gateway) receiptWorker(ctx context.Context, workerID int) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				job, ok, err := g.db.NextMediaJob(ctx, receiptImageMediaType)
				if err != nil {
					g.logger.Error("failed to claim receipt job", "worker", workerID, "error", err)
					break
				}
				if !ok {
					break
				}
				g.processReceiptJob(ctx, workerID, job)
			}
		}
	}
}

func (g gateway) processReceiptJob(parent context.Context, workerID int, job persistence.MediaJob) {
	ctx, cancel := context.WithTimeout(parent, receiptJobTimeout)
	defer cancel()

	imageData, err := os.ReadFile(job.StoragePath)
	if err != nil {
		g.failReceiptJob(ctx, job, fmt.Errorf("read temp receipt: %w", err))
		return
	}
	receiptUpload, duplicateReceipt, err := g.db.StartReceiptProcessing(ctx, job.ConversationKey, job.ProviderMessageID, job.MediaHash, job.MimeType)
	if err != nil {
		g.failReceiptJob(ctx, job, fmt.Errorf("start receipt processing: %w", err))
		return
	}
	if duplicateReceipt && (receiptUpload.ProviderMessageID != job.ProviderMessageID || receiptUpload.Status != "processing") {
		if err := g.sendTextToChat(ctx, job.ChatID, job.ConversationKey, duplicateReceiptReply(receiptUpload), "duplicate receipt reply"); err != nil {
			g.failReceiptJob(ctx, job, err)
			return
		}
		if err := g.db.MarkMediaJobSucceeded(ctx, job.ID, map[string]any{"duplicate_receipt": true, "receipt_status": receiptUpload.Status, "image_hash": job.MediaHash}); err != nil {
			g.logger.Error("failed to mark duplicate receipt job succeeded", "worker", workerID, "job_id", job.ID, "error", err)
			return
		}
		removeTempMedia(job.StoragePath)
		g.logger.Info("skipped duplicate receipt image", "worker", workerID, "job_id", job.ID, "receipt_status", receiptUpload.Status, "image_hash", job.MediaHash)
		return
	}

	conversationContext, err := inferenceContext(g.store, job.ConversationKey)
	if err != nil {
		g.failReceiptJob(ctx, job, err)
		return
	}
	parsed, err := g.inference.ParseReceipt(ctx, inference.ParseReceiptRequest{
		Source:       "whatsmeow_receipt",
		MessageID:    job.ProviderMessageID,
		MimeType:     job.MimeType,
		ImageBase64:  base64.StdEncoding.EncodeToString(imageData),
		Conversation: conversationContext,
	})
	if err != nil {
		g.failReceiptJob(ctx, job, err)
		return
	}
	if parsed.Raw == nil {
		parsed.Raw = map[string]any{}
	}
	parsed.Raw["image_hash"] = job.MediaHash
	parsed.Raw["original_message_type"] = "image"

	if err := g.db.RecordParserRun(ctx, job.ConversationKey, job.ProviderMessageID, parsed); err != nil {
		g.logger.Error("failed to record receipt parser run", "worker", workerID, "job_id", job.ID, "error", err)
	}
	if g.handleReceiptJobParseOutcome(ctx, workerID, job, parsed) {
		return
	}
	flowResult := conversation.HandleParsed(g.store, job.ConversationKey, parsed, false)
	if flowResult.Err != nil {
		if markErr := g.db.MarkReceiptFailed(ctx, job.ConversationKey, job.MediaHash); markErr != nil {
			g.logger.Error("failed to mark receipt flow failure", "worker", workerID, "job_id", job.ID, "error", markErr)
		}
		g.failReceiptJob(ctx, job, flowResult.Err)
		return
	}
	if err := g.sendTextToChat(ctx, job.ChatID, job.ConversationKey, flowResult.Reply, "receipt final reply"); err != nil {
		g.failReceiptJob(ctx, job, err)
		return
	}
	result := map[string]any{
		"image_hash": job.MediaHash,
		"intent":     parsed.Intent,
		"action":     parsed.Action,
	}
	if err := g.db.MarkMediaJobSucceeded(ctx, job.ID, result); err != nil {
		g.logger.Error("failed to mark receipt job succeeded", "worker", workerID, "job_id", job.ID, "error", err)
		return
	}
	removeTempMedia(job.StoragePath)
	g.logger.Info("processed receipt image", "worker", workerID, "job_id", job.ID, "intent", parsed.Intent, "image_hash", job.MediaHash)
}

func (g gateway) handleReceiptJobParseOutcome(ctx context.Context, workerID int, job persistence.MediaJob, parsed inference.ParseTextResponse) bool {
	if parsed.Action == "none" {
		g.logger.Info("ignored non-receipt image", "worker", workerID, "job_id", job.ID, "conversation_key", job.ConversationKey)
		if err := g.sendTextToChat(ctx, job.ChatID, job.ConversationKey, unreadableReceiptReply(), "unreadable receipt reply"); err != nil {
			g.failReceiptJob(ctx, job, err)
			return true
		}
		if err := g.db.MarkReceiptFailed(ctx, job.ConversationKey, job.MediaHash); err != nil {
			g.logger.Error("failed to mark ignored receipt", "worker", workerID, "job_id", job.ID, "error", err)
		}
		_ = g.db.MarkMediaJobSucceeded(ctx, job.ID, map[string]any{"image_hash": job.MediaHash, "action": parsed.Action})
		removeTempMedia(job.StoragePath)
		return true
	}
	if parsed.Action != "create_draft" {
		if err := g.db.MarkReceiptFailed(ctx, job.ConversationKey, job.MediaHash); err != nil {
			g.logger.Error("failed to mark unresolved receipt", "worker", workerID, "job_id", job.ID, "action", parsed.Action, "error", err)
		}
	}
	return false
}

func (g gateway) failReceiptJob(ctx context.Context, job persistence.MediaJob, err error) {
	maxAttempts := g.cfg.MediaJobMaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	terminal, markErr := g.db.MarkMediaJobFailed(ctx, job.ID, err.Error(), maxAttempts)
	if markErr != nil {
		g.logger.Error("failed to mark receipt job failed", "job_id", job.ID, "error", markErr)
		return
	}
	if !terminal {
		g.logger.Warn("receipt job will retry", "job_id", job.ID, "attempt", job.Attempts, "error", err)
		return
	}
	if markErr := g.db.MarkReceiptFailed(ctx, job.ConversationKey, job.MediaHash); markErr != nil {
		g.logger.Error("failed to mark receipt job failure", "job_id", job.ID, "error", markErr)
	}
	_ = g.sendTextToChat(ctx, job.ChatID, job.ConversationKey, unreadableReceiptReply(), "receipt failure reply")
	removeTempMedia(job.StoragePath)
	g.logger.Error("receipt job failed permanently", "job_id", job.ID, "error", err)
}

func duplicateReceiptReply(receipt persistence.ReceiptUpload) string {
	switch receipt.Status {
	case "confirmed":
		return "Struk ini sudah pernah disimpan."
	case "pending_confirmation":
		return "Struk ini sudah jadi draft. Balas simpan untuk menyimpan atau batal untuk membatalkan."
	case "processing":
		return "Struk ini sedang diproses. Tunggu sebentar ya."
	default:
		return "Struk ini sudah pernah dikirim sebelumnya."
	}
}

func unreadableReceiptReply() string {
	return "Struknya belum kebaca jelas. Kirim foto yang lebih dekat/terang, atau ketik transaksinya manual."
}
