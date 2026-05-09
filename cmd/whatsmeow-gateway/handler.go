//go:build whatsmeow

package main

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/types/events"

	"transactw/internal/conversation"
	"transactw/internal/inference"
	"transactw/internal/persistence"
	"transactw/internal/reply"
)

func (g gateway) handleEvent(rawEvt interface{}) {
	switch evt := rawEvt.(type) {
	case *events.Message:
		g.handleMessage(evt)
	}
}

func (g gateway) handleMessage(evt *events.Message) {
	if evt.Info.IsFromMe {
		return
	}
	if evt.Info.Chat.String() == "status@broadcast" {
		return
	}
	if evt.Message == nil {
		return
	}

	messageKind := "text"
	text := extractText(evt)
	image := evt.Message.GetImageMessage()
	audio := evt.Message.GetAudioMessage()
	if text == "" && image == nil && audio == nil {
		return
	}
	if image != nil {
		messageKind = "image"
		if text == "" {
			text = image.GetCaption()
		}
	} else if audio != nil {
		messageKind = "audio"
	}
	debugReply := reply.ShouldDebug(text, g.cfg.DebugJSONReplies)
	parseText := reply.StripDebugPrefix(text)
	if parseText == "" {
		parseText = text
	}
	senderID := evt.Info.Sender.String()
	if senderID == "" {
		senderID = evt.Info.Chat.String()
	}
	conversationKey := "whatsmeow:" + getenv("WHATSMEOW_SESSION_NAME", "default") + ":" + evt.Info.Chat.String()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	duplicate, err := g.db.RecordInbound(ctx, persistence.InboundMessage{
		Provider:        "whatsmeow",
		SessionName:     getenv("WHATSMEOW_SESSION_NAME", "default"),
		ConversationKey: conversationKey,
		ChatID:          evt.Info.Chat.String(),
		SenderID:        senderID,
		MessageID:       evt.Info.ID,
		MessageType:     messageKind,
		Body:            parseText,
		ProviderTime:    evt.Info.Timestamp,
	})
	if err != nil {
		g.logger.Error("failed to record inbound whatsmeow message", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		return
	}
	if duplicate {
		g.logger.Info("skipping duplicate whatsmeow message", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID)
		return
	}

	if audio != nil {
		g.enqueueVoiceNote(ctx, evt, audio, conversationKey, senderID)
		return
	}
	if image != nil {
		g.enqueueReceiptImage(ctx, evt, image, conversationKey)
		return
	}

	conversationContext, err := inferenceContext(g.store, conversationKey)
	if err != nil {
		g.logger.Error("failed to load conversation context", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
	}

	var parsed inference.ParseTextResponse
	parsed, err = g.inference.ParseText(ctx, inference.ParseTextRequest{
		Source:       "whatsmeow",
		From:         senderID,
		MessageID:    evt.Info.ID,
		Text:         parseText,
		Conversation: conversationContext,
	})
	if err != nil {
		g.logger.Error("failed to parse whatsmeow message", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "message_type", messageKind, "error", err)
		return
	}

	if err := g.db.RecordParserRun(ctx, conversationKey, evt.Info.ID, parsed); err != nil {
		g.logger.Error("failed to record parser run", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
	}

	flowResult := conversation.HandleParsed(g.store, conversationKey, parsed, debugReply)
	if flowResult.Err != nil {
		g.logger.Error("failed to handle conversation flow", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", flowResult.Err)
		return
	}

	if !g.sendAndRecordReply(ctx, evt, conversationKey, flowResult.Reply, "whatsmeow reply") {
		return
	}

	g.logger.Info(
		"replied with parse JSON",
		"chat", evt.Info.Chat.String(),
		"sender", senderID,
		"message_id", evt.Info.ID,
		"intent", parsed.Intent,
		"confidence", parsed.Confidence,
		"conversation_key", conversationKey,
		"saved_draft", flowResult.SaveDraft,
	)
}
