//go:build whatsmeow

package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types/events"

	"transactw/internal/inference"
	"transactw/internal/persistence"
)

func (g gateway) parseReceiptImage(ctx context.Context, evt *events.Message, image *waProto.ImageMessage, senderID, conversationKey, parseText string, conversationContext *inference.ConversationContext) (inference.ParseTextResponse, string, bool) {
	imageData, err := g.client.Download(ctx, image)
	if err != nil {
		g.logger.Error("failed to download whatsmeow image", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		return inference.ParseTextResponse{}, "", true
	}

	imageHash := hashBytes(imageData)
	receiptUpload, duplicateReceipt, err := g.db.StartReceiptProcessing(ctx, conversationKey, evt.Info.ID, imageHash, image.GetMimetype())
	if err != nil {
		g.logger.Error("failed to check receipt duplicate", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		return inference.ParseTextResponse{}, imageHash, true
	}
	if duplicateReceipt {
		replyBody := duplicateReceiptReply(receiptUpload)
		g.sendAndRecordReply(ctx, evt, conversationKey, replyBody, "duplicate receipt reply")
		g.logger.Info("skipped duplicate receipt image", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "receipt_status", receiptUpload.Status, "image_hash", imageHash)
		return inference.ParseTextResponse{}, imageHash, true
	}

	parsed, err := g.inference.ParseReceipt(ctx, inference.ParseReceiptRequest{
		Source:       "whatsmeow",
		From:         senderID,
		MessageID:    evt.Info.ID,
		Caption:      parseText,
		MimeType:     image.GetMimetype(),
		ImageBase64:  base64.StdEncoding.EncodeToString(imageData),
		Conversation: conversationContext,
	})
	if err != nil {
		g.logger.Error("failed to parse whatsmeow message", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "message_type", "image", "error", err)
		if markErr := g.db.MarkReceiptFailed(ctx, conversationKey, imageHash); markErr != nil {
			g.logger.Error("failed to mark receipt parse failure", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", markErr)
		}
		return inference.ParseTextResponse{}, imageHash, true
	}
	return parsed, imageHash, false
}

func (g gateway) handleReceiptParseOutcome(ctx context.Context, evt *events.Message, conversationKey, imageHash string, parsed inference.ParseTextResponse) bool {
	if parsed.Action == "none" {
		g.logger.Info("ignored non-receipt image", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "conversation_key", conversationKey)
		g.sendAndRecordReply(ctx, evt, conversationKey, unreadableReceiptReply(), "unreadable receipt reply")
		if err := g.db.MarkReceiptFailed(ctx, conversationKey, imageHash); err != nil {
			g.logger.Error("failed to mark ignored receipt", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		}
		return true
	}
	if parsed.Action != "create_draft" {
		if err := g.db.MarkReceiptFailed(ctx, conversationKey, imageHash); err != nil {
			g.logger.Error("failed to mark unresolved receipt", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "action", parsed.Action, "error", err)
		}
	}
	return false
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func duplicateReceiptReply(receipt persistence.ReceiptUpload) string {
	switch receipt.Status {
	case "confirmed":
		return "Struk ini sudah pernah disimpan."
	case "pending_confirmation":
		return "Struk ini sudah jadi draft sebelumnya. Balas `simpan` untuk simpan atau `batal` untuk batalkan."
	case "processing":
		return "Struk ini sedang diproses. Tunggu sebentar ya."
	default:
		return "Struk ini sudah pernah dikirim sebelumnya."
	}
}

func unreadableReceiptReply() string {
	return "Struknya belum kebaca jelas. Kirim foto yang lebih jelas atau lebih dekat ya."
}
