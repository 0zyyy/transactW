package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string, timeout time.Duration) Client {
	return Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

type ParseTextRequest struct {
	Source       string               `json:"source"`
	From         string               `json:"from,omitempty"`
	MessageID    string               `json:"message_id,omitempty"`
	Text         string               `json:"text"`
	Conversation *ConversationContext `json:"conversation,omitempty"`
}

type ParseReceiptRequest struct {
	Source       string               `json:"source"`
	From         string               `json:"from,omitempty"`
	MessageID    string               `json:"message_id,omitempty"`
	Caption      string               `json:"caption,omitempty"`
	MimeType     string               `json:"mime_type"`
	ImageBase64  string               `json:"image_base64"`
	Conversation *ConversationContext `json:"conversation,omitempty"`
}

type ConversationContext struct {
	HasPendingDraft bool               `json:"has_pending_draft"`
	DraftSummary    []DraftSummaryItem `json:"draft_summary,omitempty"`
	ReceiptItems    []ReceiptItem      `json:"receipt_items,omitempty"`
	LastBotPrompt   string             `json:"last_bot_prompt,omitempty"`
	State           string             `json:"state,omitempty"`
}

type DraftSummaryItem struct {
	Index       int    `json:"index"`
	Type        string `json:"type"`
	Amount      int64  `json:"amount"`
	Description string `json:"description"`
	Category    string `json:"category"`
}

type ParseTextResponse struct {
	Intent              string             `json:"intent"`
	Action              string             `json:"action"`
	ReplyDraft          string             `json:"reply_draft"`
	NeedsConfirmation   bool               `json:"needs_confirmation"`
	NeedsClarification  bool               `json:"needs_clarification"`
	ClarificationPrompt string             `json:"clarification_prompt"`
	IntentCandidates    []IntentCandidate  `json:"intent_candidates"`
	Amount              *int64             `json:"amount"`
	Currency            string             `json:"currency"`
	MerchantName        string             `json:"merchant_name"`
	Description         string             `json:"description"`
	CategoryHint        string             `json:"category_hint"`
	AccountHint         string             `json:"account_hint"`
	TransactionDate     string             `json:"transaction_date"`
	Transactions        []TransactionDraft `json:"transactions"`
	Query               *QueryDraft        `json:"query,omitempty"`
	Edit                *EditDraft         `json:"edit,omitempty"`
	Confidence          float64            `json:"confidence"`
	MissingFields       []string           `json:"missing_fields"`
	Raw                 map[string]any     `json:"raw,omitempty"`
}

type EditDraft struct {
	TargetItemIndex *int   `json:"target_item_index,omitempty"`
	Field           string `json:"field"`
	Value           any    `json:"value,omitempty"`
	Amount          *int64 `json:"amount,omitempty"`
	CategoryHint    string `json:"category_hint,omitempty"`
	Description     string `json:"description,omitempty"`
}

type IntentCandidate struct {
	Intent     string  `json:"intent"`
	Score      float64 `json:"score"`
	Reason     string  `json:"reason"`
	NeedsReply bool    `json:"needs_reply"`
}

type TransactionDraft struct {
	Type            string `json:"type"`
	Amount          int64  `json:"amount"`
	Currency        string `json:"currency"`
	MerchantName    string `json:"merchant_name"`
	Description     string `json:"description"`
	CategoryHint    string `json:"category_hint"`
	AccountHint     string `json:"account_hint"`
	TransactionDate string `json:"transaction_date"`
}

type QueryDraft struct {
	Metric              string    `json:"metric"`
	Type                string    `json:"type"`
	DateRange           DateRange `json:"date_range"`
	NeedsClarification  bool      `json:"needs_clarification"`
	ClarificationPrompt string    `json:"clarification_prompt"`
}

type DateRange struct {
	RawText    string  `json:"raw_text"`
	Preset     string  `json:"preset"`
	StartDate  string  `json:"start_date"`
	EndDate    string  `json:"end_date"`
	Confidence float64 `json:"confidence"`
}

func (c Client) ParseText(ctx context.Context, req ParseTextRequest) (ParseTextResponse, error) {
	return c.parse(ctx, "/v1/parse/text", req)
}

func (c Client) ParseReceipt(ctx context.Context, req ParseReceiptRequest) (ParseTextResponse, error) {
	return c.parse(ctx, "/v1/parse/receipt", req)
}

func (c Client) parse(ctx context.Context, path string, req any) (ParseTextResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return ParseTextResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return ParseTextResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return ParseTextResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ParseTextResponse{}, fmt.Errorf("inference service returned status %d", resp.StatusCode)
	}

	var parsed ParseTextResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return ParseTextResponse{}, err
	}
	return parsed, nil
}
