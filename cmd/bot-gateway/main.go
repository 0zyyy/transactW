package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"transactw/internal/config"
	"transactw/internal/conversation"
	"transactw/internal/httpjson"
	"transactw/internal/inference"
	"transactw/internal/persistence"
	"transactw/internal/reply"
	"transactw/internal/whatsapp"
)

type server struct {
	cfg       config.Config
	inference inference.Client
	whatsapp  whatsapp.CloudClient
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

	s := server{
		cfg:       cfg,
		inference: inference.NewClient(cfg.InferenceURL, cfg.InferenceTimeout),
		whatsapp:  whatsapp.NewCloudClient(cfg.WhatsAppAccessToken, cfg.WhatsAppPhoneID, cfg.WhatsAppGraphAPI, cfg.InferenceTimeout),
		store:     db,
		db:        db,
		logger:    logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /webhook/whatsapp", s.verifyWebhook)
	mux.HandleFunc("POST /webhook/whatsapp", s.receiveWebhook)
	mux.HandleFunc("POST /debug/parse", s.debugParse)

	addr := ":" + cfg.Port
	logger.Info("starting bot gateway", "addr", addr, "inference_url", cfg.InferenceURL)
	if err := http.ListenAndServe(addr, mux); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func (s server) health(w http.ResponseWriter, r *http.Request) {
	httpjson.Write(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s server) verifyWebhook(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("hub.mode")
	token := r.URL.Query().Get("hub.verify_token")
	challenge := r.URL.Query().Get("hub.challenge")

	if mode != "subscribe" {
		httpjson.Error(w, http.StatusBadRequest, "unsupported webhook mode")
		return
	}
	if s.cfg.WhatsAppVerifyToken != "" && token != s.cfg.WhatsAppVerifyToken {
		httpjson.Error(w, http.StatusForbidden, "invalid verify token")
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(challenge))
}

func (s server) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	var payload whatsapp.WebhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		httpjson.Error(w, http.StatusBadRequest, "invalid webhook JSON")
		return
	}

	inbound := whatsapp.ExtractInboundText(payload)
	results := make([]map[string]any, 0, len(inbound))
	for _, msg := range inbound {
		debugReply := reply.ShouldDebug(msg.Text, s.cfg.DebugJSONReplies)
		parseText := reply.StripDebugPrefix(msg.Text)
		if parseText == "" {
			parseText = msg.Text
		}
		conversationKey := "meta:" + msg.PhoneNumberID + ":" + msg.From
		duplicate, err := s.db.RecordInbound(r.Context(), persistence.InboundMessage{
			Provider:        "meta_cloud_api",
			SessionName:     msg.PhoneNumberID,
			ConversationKey: conversationKey,
			ChatID:          msg.From,
			SenderID:        msg.From,
			MessageID:       msg.MessageID,
			MessageType:     "text",
			Body:            parseText,
			ProviderTime:    parseProviderTimestamp(msg.Timestamp),
		})
		if err != nil {
			s.logger.Error("failed to record inbound text", "message_id", msg.MessageID, "from", msg.From, "error", err)
			results = append(results, map[string]any{
				"message_id": msg.MessageID,
				"status":     "persist_failed",
				"error":      err.Error(),
			})
			continue
		}
		if duplicate {
			results = append(results, map[string]any{
				"message_id": msg.MessageID,
				"status":     "duplicate_skipped",
			})
			continue
		}
		parsed, err := s.inference.ParseText(r.Context(), inference.ParseTextRequest{
			Source:    "whatsapp",
			From:      msg.From,
			MessageID: msg.MessageID,
			Text:      parseText,
		})
		if err != nil {
			s.logger.Error("failed to parse inbound text", "message_id", msg.MessageID, "from", msg.From, "error", err)
			results = append(results, map[string]any{
				"message_id": msg.MessageID,
				"status":     "parse_failed",
				"error":      err.Error(),
			})
			continue
		}
		if err := s.db.RecordParserRun(r.Context(), conversationKey, msg.MessageID, parsed); err != nil {
			s.logger.Error("failed to record parser run", "message_id", msg.MessageID, "from", msg.From, "error", err)
		}

		flowResult := conversation.HandleParsed(s.store, conversationKey, parsed, debugReply)
		if flowResult.Err != nil {
			s.logger.Error("failed to handle conversation flow", "message_id", msg.MessageID, "from", msg.From, "error", flowResult.Err)
			results = append(results, map[string]any{
				"message_id": msg.MessageID,
				"status":     "flow_failed",
				"parse":      parsed,
				"error":      flowResult.Err.Error(),
			})
			continue
		}
		replyBody := flowResult.Reply

		replyStatus := "skipped_not_configured"
		if s.whatsapp.Configured() {
			sendResp, err := s.whatsapp.SendText(r.Context(), msg.From, replyBody)
			if err != nil {
				s.logger.Error("failed to send whatsapp debug reply", "message_id", msg.MessageID, "from", msg.From, "error", err)
				results = append(results, map[string]any{
					"message_id": msg.MessageID,
					"status":     "reply_failed",
					"parse":      parsed,
					"error":      err.Error(),
				})
				continue
			}
			replyStatus = "sent"
			s.logger.Info("sent whatsapp debug reply", "message_id", msg.MessageID, "from", msg.From, "outbound_messages", len(sendResp.Messages))
			if err := s.db.RecordOutbound(r.Context(), persistence.OutboundMessage{
				Provider:        "meta_cloud_api",
				SessionName:     msg.PhoneNumberID,
				ConversationKey: conversationKey,
				ChatID:          msg.From,
				Body:            replyBody,
			}); err != nil {
				s.logger.Error("failed to record outbound text", "message_id", msg.MessageID, "from", msg.From, "error", err)
			}
		} else {
			s.logger.Info("whatsapp outbound not configured; skipping reply", "message_id", msg.MessageID, "from", msg.From)
		}

		s.logger.Info(
			"parsed inbound text",
			"message_id", msg.MessageID,
			"from", msg.From,
			"intent", parsed.Intent,
			"amount", parsed.Amount,
			"confidence", parsed.Confidence,
			"conversation_key", conversationKey,
			"saved_draft", flowResult.SaveDraft,
		)
		results = append(results, map[string]any{
			"message_id":   msg.MessageID,
			"status":       "parsed",
			"reply_status": replyStatus,
			"parse":        parsed,
		})
	}

	httpjson.Write(w, http.StatusOK, map[string]any{
		"status":   "accepted",
		"messages": results,
	})
}

func (s server) debugParse(w http.ResponseWriter, r *http.Request) {
	var req inference.ParseTextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpjson.Error(w, http.StatusBadRequest, "invalid request JSON")
		return
	}
	if err := validateDebugParse(req); err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Source == "" {
		req.Source = "debug"
	}

	parsed, err := s.inference.ParseText(r.Context(), req)
	if err != nil {
		s.logger.Error("debug parse failed", "error", err)
		httpjson.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	httpjson.Write(w, http.StatusOK, parsed)
}

func validateDebugParse(req inference.ParseTextRequest) error {
	if req.Text == "" {
		return errors.New("text is required")
	}
	return nil
}

func parseProviderTimestamp(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}
