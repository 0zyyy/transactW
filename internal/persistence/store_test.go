package persistence

import (
	"context"
	"os"
	"testing"
	"time"

	"transactw/internal/inference"
)

func TestStorePersistsDraftAndConfirmsTransaction(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	store, err := Open(ctx, dsn, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	suffix := time.Now().UnixNano()
	inbound := InboundMessage{
		Provider:        "whatsmeow",
		SessionName:     "test",
		ConversationKey: "whatsmeow:test:628123-" + time.Unix(0, suffix).Format("150405.000000000") + "@s.whatsapp.net",
		ChatID:          "628123@s.whatsapp.net",
		SenderID:        "628123@s.whatsapp.net",
		MessageID:       "wamid.test." + time.Unix(0, suffix).Format("150405.000000000"),
		MessageType:     "text",
		Body:            "makan 25000 nasi padang",
	}
	duplicate, err := store.RecordInbound(ctx, inbound)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("first inbound message should not be duplicate")
	}
	duplicate, err = store.RecordInbound(ctx, inbound)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate {
		t.Fatal("second inbound message should be duplicate")
	}

	amount := int64(25000)
	parsed := inference.ParseTextResponse{
		Intent:          "create_expense",
		Amount:          &amount,
		Currency:        "IDR",
		Description:     "makan nasi padang",
		CategoryHint:    "Makan & Minum",
		TransactionDate: "2026-04-29",
		Confidence:      0.91,
	}
	if _, err := store.Save(inbound.ConversationKey, parsed); err != nil {
		t.Fatal(err)
	}
	draft, ok, err := store.Confirm(inbound.ConversationKey)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected pending draft")
	}
	if draft.Parsed.Description != parsed.Description {
		t.Fatalf("draft description mismatch: got %q", draft.Parsed.Description)
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE description = $1`, parsed.Description).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 1 {
		t.Fatalf("expected at least 1 confirmed transaction, got %d", count)
	}

	var userID string
	if err := store.db.QueryRow(`SELECT user_id FROM whatsapp_conversations WHERE conversation_key = $1`, inbound.ConversationKey).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if userID == "" || userID == defaultUserID {
		t.Fatalf("expected sender-specific user id, got %q", userID)
	}
	var txUserID string
	if err := store.db.QueryRow(`SELECT user_id FROM transactions WHERE description = $1 ORDER BY created_at DESC LIMIT 1`, parsed.Description).Scan(&txUserID); err != nil {
		t.Fatal(err)
	}
	if txUserID != userID {
		t.Fatalf("transaction user mismatch: got %q, want %q", txUserID, userID)
	}

	startDate, err := time.Parse("2006-01-02", "2026-04-01")
	if err != nil {
		t.Fatal(err)
	}
	endDate, err := time.Parse("2006-01-02", "2026-04-30")
	if err != nil {
		t.Fatal(err)
	}
	total, err := store.ExpenseTotal(ctx, userID, startDate, endDate)
	if err != nil {
		t.Fatal(err)
	}
	if total < amount {
		t.Fatalf("expected expense total >= %d, got %d", amount, total)
	}
	transactions, err := store.RecentTransactions(ctx, userID, startDate, endDate, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(transactions) == 0 {
		t.Fatal("expected at least one recent transaction")
	}
}
