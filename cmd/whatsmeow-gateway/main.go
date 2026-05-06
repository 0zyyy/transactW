//go:build whatsmeow

package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

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

	storePath := getenv("WHATSMEOW_STORE_PATH", "file:whatsmeow-session.db?_pragma=foreign_keys(1)")
	dbLog := waLog.Stdout("WhatsmeowDB", getenv("WHATSMEOW_LOG_LEVEL", "INFO"), true)
	container, err := sqlstore.New(ctx, "sqlite", storePath, dbLog)
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
	if evt.Info.Chat.String() == "status@broadcast" {
		return
	}
	if evt.Message == nil {
		return
	}

	messageKind := "text"
	text := extractText(evt)
	image := evt.Message.GetImageMessage()
	if text == "" && image == nil {
		return
	}
	if image != nil {
		messageKind = "image"
		if text == "" {
			text = image.GetCaption()
		}
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

	conversationContext, err := inferenceContext(g.store, conversationKey)
	if err != nil {
		g.logger.Error("failed to load conversation context", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
	}

	var parsed inference.ParseTextResponse
	imageHash := ""
	if image != nil {
		imageData, err := g.client.Download(ctx, image)
		if err != nil {
			g.logger.Error("failed to download whatsmeow image", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
			return
		}
		imageHash = hashBytes(imageData)
		receiptUpload, duplicateReceipt, err := g.db.StartReceiptProcessing(ctx, conversationKey, evt.Info.ID, imageHash, image.GetMimetype())
		if err != nil {
			g.logger.Error("failed to check receipt duplicate", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
			return
		}
		if duplicateReceipt {
			replyBody := duplicateReceiptReply(receiptUpload)
			_, err = g.client.SendMessage(ctx, evt.Info.Chat, &waProto.Message{Conversation: proto.String(replyBody)})
			if err != nil {
				g.logger.Error("failed to send duplicate receipt reply", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
				return
			}
			if err := g.db.RecordOutbound(ctx, persistence.OutboundMessage{
				Provider:        "whatsmeow",
				SessionName:     getenv("WHATSMEOW_SESSION_NAME", "default"),
				ConversationKey: conversationKey,
				ChatID:          evt.Info.Chat.String(),
				Body:            replyBody,
			}); err != nil {
				g.logger.Error("failed to record duplicate receipt outbound message", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
			}
			g.logger.Info("skipped duplicate receipt image", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "receipt_status", receiptUpload.Status, "image_hash", imageHash)
			return
		}
		parsed, err = g.inference.ParseReceipt(ctx, inference.ParseReceiptRequest{
			Source:       "whatsmeow",
			From:         senderID,
			MessageID:    evt.Info.ID,
			Caption:      parseText,
			MimeType:     image.GetMimetype(),
			ImageBase64:  base64.StdEncoding.EncodeToString(imageData),
			Conversation: conversationContext,
		})
	} else {
		parsed, err = g.inference.ParseText(ctx, inference.ParseTextRequest{
			Source:       "whatsmeow",
			From:         senderID,
			MessageID:    evt.Info.ID,
			Text:         parseText,
			Conversation: conversationContext,
		})
	}
	if err != nil {
		g.logger.Error("failed to parse whatsmeow message", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "message_type", messageKind, "error", err)
		if imageHash != "" {
			if markErr := g.db.MarkReceiptFailed(ctx, conversationKey, imageHash); markErr != nil {
				g.logger.Error("failed to mark receipt parse failure", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", markErr)
			}
		}
		return
	}
	if imageHash != "" {
		if parsed.Raw == nil {
			parsed.Raw = map[string]any{}
		}
		parsed.Raw["image_hash"] = imageHash
	}
	if err := g.db.RecordParserRun(ctx, conversationKey, evt.Info.ID, parsed); err != nil {
		g.logger.Error("failed to record parser run", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
	}
	if image != nil && parsed.Action == "none" {
		g.logger.Info("ignored non-receipt image", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "conversation_key", conversationKey)
		replyBody := unreadableReceiptReply()
		_, err = g.client.SendMessage(ctx, evt.Info.Chat, &waProto.Message{Conversation: proto.String(replyBody)})
		if err != nil {
			g.logger.Error("failed to send unreadable receipt reply", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
			return
		}
		if err := g.db.RecordOutbound(ctx, persistence.OutboundMessage{
			Provider:        "whatsmeow",
			SessionName:     getenv("WHATSMEOW_SESSION_NAME", "default"),
			ConversationKey: conversationKey,
			ChatID:          evt.Info.Chat.String(),
			Body:            replyBody,
		}); err != nil {
			g.logger.Error("failed to record unreadable receipt outbound message", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		}
		if err := g.db.MarkReceiptFailed(ctx, conversationKey, imageHash); err != nil {
			g.logger.Error("failed to mark ignored receipt", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", err)
		}
		return
	}
	if image != nil && parsed.Action != "create_draft" {
		if err := g.db.MarkReceiptFailed(ctx, conversationKey, imageHash); err != nil {
			g.logger.Error("failed to mark unresolved receipt", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "action", parsed.Action, "error", err)
		}
	}

	flowResult := conversation.HandleParsed(g.store, conversationKey, parsed, debugReply)
	if flowResult.Err != nil {
		g.logger.Error("failed to handle conversation flow", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", flowResult.Err)
		if imageHash != "" {
			if markErr := g.db.MarkReceiptFailed(ctx, conversationKey, imageHash); markErr != nil {
				g.logger.Error("failed to mark receipt flow failure", "chat", evt.Info.Chat.String(), "message_id", evt.Info.ID, "error", markErr)
			}
		}
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
		ReceiptItems:    inference.ReceiptItems(draft.Parsed),
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
