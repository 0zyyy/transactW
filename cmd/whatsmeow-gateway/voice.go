//go:build whatsmeow

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"transactw/internal/conversation"
	"transactw/internal/inference"
	"transactw/internal/persistence"
)

const voiceNoteMediaType = "voice_note"

func (g gateway) enqueueVoiceNote(ctx context.Context, evt *events.Message, audio *waProto.AudioMessage, conversationKey, senderID string) bool {
	if !g.cfg.VoiceNoteEnabled {
		return g.sendAndRecordReply(ctx, evt, conversationKey, "Voice note belum aktif. Ketik transaksinya dulu ya.", "voice disabled reply")
	}
	pending, err := g.db.PendingMediaJobCount(ctx, voiceNoteMediaType)
	if err != nil {
		g.logger.Error("failed to count voice jobs", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		return false
	}
	if g.cfg.MediaQueueMaxPending > 0 && pending >= g.cfg.MediaQueueMaxPending {
		return g.sendAndRecordReply(ctx, evt, conversationKey, "Lagi banyak voice note yang diproses. Coba lagi nanti atau ketik transaksinya.", "voice queue full reply")
	}

	audioData, err := g.client.Download(ctx, audio)
	if err != nil {
		g.logger.Error("failed to download voice note", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		return g.sendAndRecordReply(ctx, evt, conversationKey, "Voice note belum bisa diproses. Coba kirim ulang atau ketik transaksinya.", "voice download failure reply")
	}
	if g.cfg.VoiceNoteMaxBytes > 0 && int64(len(audioData)) > g.cfg.VoiceNoteMaxBytes {
		return g.sendAndRecordReply(ctx, evt, conversationKey, "Voice note terlalu panjang. Coba kirim yang lebih pendek atau ketik transaksinya.", "voice too large reply")
	}
	storagePath, mediaHash, err := writeTempMedia(g.cfg.MediaTempDir, voiceNoteMediaType, evt.Info.ID, audioData)
	if err != nil {
		g.logger.Error("failed to store voice note temp file", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		return false
	}

	job, duplicate, err := g.db.CreateMediaJob(ctx, persistence.CreateMediaJobInput{
		ConversationKey:   conversationKey,
		ProviderMessageID: evt.Info.ID,
		ChatID:            evt.Info.Chat.String(),
		MediaType:         voiceNoteMediaType,
		MimeType:          audio.GetMimetype(),
		StoragePath:       storagePath,
		MediaHash:         mediaHash,
	})
	if err != nil {
		removeTempMedia(storagePath)
		g.logger.Error("failed to create voice media job", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		return false
	}
	if duplicate {
		removeTempMedia(storagePath)
		message := "Voice note ini sudah diterima."
		if job.Status == "queued" || job.Status == "processing" {
			message = "Voice note ini masih diproses."
		}
		return g.sendAndRecordReply(ctx, evt, conversationKey, message, "duplicate voice job reply")
	}
	g.logger.Info("queued voice note", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "sender", senderID, "job_id", job.ID)
	return g.sendAndRecordReply(ctx, evt, conversationKey, "Voice note diterima, sedang diproses.", "voice queued reply")
}

func (g gateway) startVoiceWorkers(ctx context.Context) {
	if !g.cfg.VoiceNoteEnabled {
		return
	}
	concurrency := g.cfg.VoiceWorkerConcurrency
	if concurrency < 1 {
		concurrency = 1
	}
	for index := 0; index < concurrency; index++ {
		go g.voiceWorker(ctx, index+1)
	}
}

func (g gateway) voiceWorker(ctx context.Context, workerID int) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				job, ok, err := g.db.NextMediaJob(ctx, voiceNoteMediaType)
				if err != nil {
					g.logger.Error("failed to claim voice job", "worker", workerID, "error", err)
					break
				}
				if !ok {
					break
				}
				g.processVoiceJob(ctx, workerID, job)
			}
		}
	}
}

func (g gateway) processVoiceJob(parent context.Context, workerID int, job persistence.MediaJob) {
	ctx, cancel := context.WithTimeout(parent, g.cfg.VoiceJobTimeout)
	defer cancel()

	audioData, err := os.ReadFile(job.StoragePath)
	if err != nil {
		g.failVoiceJob(ctx, job, fmt.Errorf("read temp audio: %w", err))
		return
	}
	transcribed, err := g.inference.TranscribeAudio(ctx, inference.TranscribeAudioRequest{
		Source:      "whatsmeow",
		MessageID:   job.ProviderMessageID,
		MimeType:    job.MimeType,
		AudioBase64: base64.StdEncoding.EncodeToString(audioData),
	})
	if err != nil {
		g.failVoiceJob(ctx, job, err)
		return
	}
	transcript := strings.TrimSpace(transcribed.Transcript)
	if transcript == "" || transcribed.Confidence < 0.35 {
		g.failVoiceJob(ctx, job, fmt.Errorf("low confidence transcript %.2f", transcribed.Confidence))
		return
	}

	conversationContext, err := inferenceContext(g.store, job.ConversationKey)
	if err != nil {
		g.failVoiceJob(ctx, job, err)
		return
	}
	parsed, err := g.inference.ParseText(ctx, inference.ParseTextRequest{
		Source:       "whatsmeow_voice",
		MessageID:    job.ProviderMessageID,
		Text:         transcript,
		Conversation: conversationContext,
	})
	if err != nil {
		g.failVoiceJob(ctx, job, err)
		return
	}
	if parsed.Raw == nil {
		parsed.Raw = map[string]any{}
	}
	parsed.Raw["audio_transcript"] = transcript
	parsed.Raw["audio_provider"] = transcribed.Provider
	parsed.Raw["original_message_type"] = "audio"

	if err := g.db.RecordParserRun(ctx, job.ConversationKey, job.ProviderMessageID, parsed); err != nil {
		g.logger.Error("failed to record voice parser run", "worker", workerID, "job_id", job.ID, "error", err)
	}
	flowResult := conversation.HandleParsed(g.store, job.ConversationKey, parsed, false)
	if flowResult.Err != nil {
		g.failVoiceJob(ctx, job, flowResult.Err)
		return
	}
	if err := g.sendTextToChat(ctx, job.ChatID, job.ConversationKey, flowResult.Reply, "voice final reply"); err != nil {
		g.failVoiceJob(ctx, job, err)
		return
	}
	result := map[string]any{
		"transcript": transcript,
		"confidence": transcribed.Confidence,
		"provider":   transcribed.Provider,
		"intent":     parsed.Intent,
		"action":     parsed.Action,
	}
	if err := g.db.MarkMediaJobSucceeded(ctx, job.ID, result); err != nil {
		g.logger.Error("failed to mark voice job succeeded", "worker", workerID, "job_id", job.ID, "error", err)
		return
	}
	removeTempMedia(job.StoragePath)
	g.logger.Info("processed voice note", "worker", workerID, "job_id", job.ID, "intent", parsed.Intent)
}

func (g gateway) failVoiceJob(ctx context.Context, job persistence.MediaJob, err error) {
	maxAttempts := g.cfg.MediaJobMaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	terminal, markErr := g.db.MarkMediaJobFailed(ctx, job.ID, err.Error(), maxAttempts)
	if markErr != nil {
		g.logger.Error("failed to mark voice job failed", "job_id", job.ID, "error", markErr)
		return
	}
	if !terminal {
		g.logger.Warn("voice job will retry", "job_id", job.ID, "attempt", job.Attempts, "error", err)
		return
	}
	_ = g.sendTextToChat(ctx, job.ChatID, job.ConversationKey, "Aku belum yakin isi voice note-nya. Bisa ketik transaksinya?", "voice failure reply")
	removeTempMedia(job.StoragePath)
	g.logger.Error("voice job failed permanently", "job_id", job.ID, "error", err)
}

func (g gateway) sendTextToChat(ctx context.Context, chatID, conversationKey, body, logLabel string) error {
	jid, err := types.ParseJID(chatID)
	if err != nil {
		return err
	}
	_, err = g.client.SendMessage(ctx, jid, &waProto.Message{Conversation: proto.String(body)})
	if err != nil {
		return err
	}
	if err := g.db.RecordOutbound(ctx, persistence.OutboundMessage{
		Provider:        "whatsmeow",
		SessionName:     getenv("WHATSMEOW_SESSION_NAME", "default"),
		ConversationKey: conversationKey,
		ChatID:          chatID,
		Body:            body,
	}); err != nil {
		g.logger.Error("failed to record outbound "+logLabel, "chat", chatID, "error", err)
	}
	return nil
}
