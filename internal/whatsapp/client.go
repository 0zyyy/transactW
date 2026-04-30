package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type CloudClient struct {
	accessToken   string
	phoneNumberID string
	graphVersion  string
	httpClient    *http.Client
}

func NewCloudClient(accessToken, phoneNumberID, graphVersion string, timeout time.Duration) CloudClient {
	return CloudClient{
		accessToken:   accessToken,
		phoneNumberID: phoneNumberID,
		graphVersion:  strings.TrimPrefix(defaultString(graphVersion, "v21.0"), "/"),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c CloudClient) Configured() bool {
	return c.accessToken != "" && c.phoneNumberID != ""
}

type SendMessageResponse struct {
	MessagingProduct string `json:"messaging_product"`
	Contacts         []struct {
		Input string `json:"input"`
		WaID  string `json:"wa_id"`
	} `json:"contacts"`
	Messages []struct {
		ID string `json:"id"`
	} `json:"messages"`
}

func (c CloudClient) SendText(ctx context.Context, to, body string) (SendMessageResponse, error) {
	if !c.Configured() {
		return SendMessageResponse{}, fmt.Errorf("whatsapp cloud client is not configured")
	}

	payload := map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                to,
		"type":              "text",
		"text": map[string]any{
			"preview_url": false,
			"body":        truncateText(body, 4000),
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return SendMessageResponse{}, err
	}

	url := fmt.Sprintf("https://graph.facebook.com/%s/%s/messages", c.graphVersion, c.phoneNumberID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return SendMessageResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return SendMessageResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errorBody map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&errorBody)
		return SendMessageResponse{}, fmt.Errorf("whatsapp send returned status %d: %v", resp.StatusCode, errorBody)
	}

	var sendResp SendMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&sendResp); err != nil {
		return SendMessageResponse{}, err
	}
	return sendResp, nil
}

func truncateText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n... truncated"
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
