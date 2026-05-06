//go:build whatsmeow

package main

import (
	"context"
	"os"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"transactw/internal/persistence"
)

func (g gateway) sendAndRecordReply(ctx context.Context, evt *events.Message, conversationKey, body, logLabel string) bool {
	_, err := g.client.SendMessage(ctx, evt.Info.Chat, &waProto.Message{Conversation: proto.String(body)})
	if err != nil {
		g.logger.Error("failed to send "+logLabel, "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		return false
	}
	if err := g.db.RecordOutbound(ctx, persistence.OutboundMessage{
		Provider:        "whatsmeow",
		SessionName:     getenv("WHATSMEOW_SESSION_NAME", "default"),
		ConversationKey: conversationKey,
		ChatID:          evt.Info.Chat.String(),
		Body:            body,
	}); err != nil {
		g.logger.Error("failed to record outbound "+logLabel, "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
	}
	return true
}

func extractText(evt *events.Message) string {
	if evt.Message == nil {
		return ""
	}
	if text := evt.Message.GetConversation(); text != "" {
		return text
	}
	if extended := evt.Message.GetExtendedTextMessage(); extended != nil {
		return extended.GetText()
	}
	return ""
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
