//go:build whatsmeow

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"transactw/internal/config"
	"transactw/internal/conversation"
	"transactw/internal/inference"
	"transactw/internal/persistence"
	"transactw/internal/reply"
)

type gateway struct {
	client    *whatsmeow.Client
	cfg       config.Config
	inference inference.Client
	store     conversation.DraftStore
	db        *persistence.Store
	logger    *slog.Logger
}

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ctx := context.Background()
	db, err := persistence.Open(ctx, cfg.DatabaseDSN, 30*time.Minute)
	if err != nil {
		logger.Error("failed to open persistence database", "dsn", cfg.DatabaseDSN, "error", err)
		os.Exit(1)
	}
	defer db.Close()

	storePath := getenv("WHATSMEOW_STORE_PATH", "file:whatsmeow-session.db?_foreign_keys=on")
	dbLog := waLog.Stdout("WhatsmeowDB", getenv("WHATSMEOW_LOG_LEVEL", "INFO"), true)
	container, err := sqlstore.New(ctx, "sqlite3", storePath, dbLog)
	if err != nil {
		logger.Error("failed to open whatsmeow store", "error", err)
		os.Exit(1)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		logger.Error("failed to load whatsmeow device", "error", err)
		os.Exit(1)
	}

	clientLog := waLog.Stdout("Whatsmeow", getenv("WHATSMEOW_LOG_LEVEL", "INFO"), true)
	client := whatsmeow.NewClient(deviceStore, clientLog)
	gw := gateway{
		client:    client,
		cfg:       cfg,
		inference: inference.NewClient(cfg.InferenceURL, cfg.InferenceTimeout),
		store:     db,
		db:        db,
		logger:    logger,
	}
	client.AddEventHandler(gw.handleEvent)

	if client.Store.ID == nil {
		qrChan, err := client.GetQRChannel(ctx)
		if err != nil {
			logger.Error("failed to create QR channel", "error", err)
			os.Exit(1)
		}
		if err := client.Connect(); err != nil {
			logger.Error("failed to connect whatsmeow", "error", err)
			os.Exit(1)
		}
		for evt := range qrChan {
			if evt.Event == "code" {
				logger.Info("scan this QR with WhatsApp linked devices")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			} else {
				logger.Info("whatsmeow login event", "event", evt.Event)
			}
		}
	} else {
		if err := client.Connect(); err != nil {
			logger.Error("failed to connect whatsmeow", "error", err)
			os.Exit(1)
		}
	}

	logger.Info("whatsmeow gateway started", "inference_url", cfg.InferenceURL)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Info("stopping whatsmeow gateway")
	client.Disconnect()
}

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

	text := extractText(evt)
	if text == "" {
		return
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
		MessageType:     "text",
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

	conversationContext, err := inferenceContext(g.store, conversationKey)
	if err != nil {
		g.logger.Error("failed to load conversation context", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
	}

	parsed, err := g.inference.ParseText(ctx, inference.ParseTextRequest{
		Source:       "whatsmeow",
		From:         senderID,
		MessageID:    evt.Info.ID,
		Text:         parseText,
		Conversation: conversationContext,
	})
	if err != nil {
		g.logger.Error("failed to parse whatsmeow message", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		_, _ = g.client.SendMessage(ctx, evt.Info.Chat, &waProto.Message{
			Conversation: proto.String("parse error: " + err.Error()),
		})
		return
	}
	if err := g.db.RecordParserRun(ctx, conversationKey, evt.Info.ID, parsed); err != nil {
		g.logger.Error("failed to record parser run", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
	}

	flowResult := conversation.HandleParsed(g.store, conversationKey, parsed, debugReply)
	if flowResult.Err != nil {
		g.logger.Error("failed to handle conversation flow", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", flowResult.Err)
		_, _ = g.client.SendMessage(ctx, evt.Info.Chat, &waProto.Message{
			Conversation: proto.String("database error: " + flowResult.Err.Error()),
		})
		return
	}
	replyBody := flowResult.Reply

	_, err = g.client.SendMessage(ctx, evt.Info.Chat, &waProto.Message{
		Conversation: proto.String(replyBody),
	})
	if err != nil {
		g.logger.Error("failed to send whatsmeow reply", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		return
	}
	if err := g.db.RecordOutbound(ctx, persistence.OutboundMessage{
		Provider:        "whatsmeow",
		SessionName:     getenv("WHATSMEOW_SESSION_NAME", "default"),
		ConversationKey: conversationKey,
		ChatID:          evt.Info.Chat.String(),
		Body:            replyBody,
	}); err != nil {
		g.logger.Error("failed to record outbound whatsmeow message", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
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

func inferenceContext(store conversation.DraftStore, conversationKey string) (*inference.ConversationContext, error) {
	draft, ok, err := store.Get(conversationKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &inference.ConversationContext{HasPendingDraft: false, State: "idle"}, nil
	}
	return &inference.ConversationContext{
		HasPendingDraft: true,
		State:           "pending_confirmation",
		DraftSummary:    draftSummary(draft.Parsed),
		LastBotPrompt:   "Balas simpan/batal atau kirim koreksi.",
	}, nil
}

func draftSummary(parsed inference.ParseTextResponse) []inference.DraftSummaryItem {
	if len(parsed.Transactions) > 0 {
		items := make([]inference.DraftSummaryItem, 0, len(parsed.Transactions))
		for index, tx := range parsed.Transactions {
			items = append(items, inference.DraftSummaryItem{
				Index:       index + 1,
				Type:        tx.Type,
				Amount:      tx.Amount,
				Description: tx.Description,
				Category:    tx.CategoryHint,
			})
		}
		return items
	}
	amount := int64(0)
	if parsed.Amount != nil {
		amount = *parsed.Amount
	}
	return []inference.DraftSummaryItem{{
		Index:       1,
		Type:        draftType(parsed.Intent),
		Amount:      amount,
		Description: parsed.Description,
		Category:    parsed.CategoryHint,
	}}
}

func draftType(intent string) string {
	switch intent {
	case "create_income":
		return "income"
	case "create_multiple_transactions":
		return "multiple"
	default:
		return "expense"
	}
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
