package persistence

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"transactw/internal/conversation"
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

func TestStoreConfirmIsIdempotentAfterDraftIsConfirmed(t *testing.T) {
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
	conversationKey := "whatsmeow:test:confirm-idempotent-" + time.Unix(0, suffix).Format("150405.000000000") + "@s.whatsapp.net"
	description := "idempotent confirm nasi padang " + time.Unix(0, suffix).Format("150405.000000000")
	inbound := InboundMessage{
		Provider:        "whatsmeow",
		SessionName:     "test",
		ConversationKey: conversationKey,
		ChatID:          "628123@s.whatsapp.net",
		SenderID:        "628123@s.whatsapp.net",
		MessageID:       "wamid.confirm-idempotent." + time.Unix(0, suffix).Format("150405.000000000"),
		MessageType:     "text",
		Body:            description,
	}
	if duplicate, err := store.RecordInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	} else if duplicate {
		t.Fatal("first inbound message should not be duplicate")
	}

	amount := int64(25000)
	parsed := inference.ParseTextResponse{
		Intent:          "create_expense",
		Amount:          &amount,
		Currency:        "IDR",
		Description:     description,
		CategoryHint:    "Makan & Minum",
		TransactionDate: "2026-04-29",
		Confidence:      0.91,
	}
	if _, err := store.Save(conversationKey, parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Confirm(conversationKey); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("expected first confirm to find pending draft")
	}
	if _, ok, err := store.Confirm(conversationKey); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("second confirm should not find an already confirmed draft")
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE description = $1`, description).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 transaction after duplicate confirm, got %d", count)
	}
}

func TestStoreConcurrentConfirmOnlyCreatesOneTransaction(t *testing.T) {
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
	stamp := time.Unix(0, suffix).Format("150405.000000000")
	conversationKey := "whatsmeow:test:confirm-concurrent-" + stamp + "@s.whatsapp.net"
	description := "concurrent confirm nasi padang " + stamp
	if duplicate, err := store.RecordInbound(ctx, InboundMessage{
		Provider:        "whatsmeow",
		SessionName:     "test",
		ConversationKey: conversationKey,
		ChatID:          "confirm-concurrent@s.whatsapp.net",
		SenderID:        "confirm-concurrent@s.whatsapp.net",
		MessageID:       "wamid.confirm-concurrent." + stamp,
		MessageType:     "text",
		Body:            description,
	}); err != nil {
		t.Fatal(err)
	} else if duplicate {
		t.Fatal("first inbound message should not be duplicate")
	}

	amount := int64(25000)
	if _, err := store.Save(conversationKey, inference.ParseTextResponse{
		Intent:          "create_expense",
		Amount:          &amount,
		Currency:        "IDR",
		Description:     description,
		CategoryHint:    "Makan & Minum",
		TransactionDate: "2026-04-29",
		Confidence:      0.91,
	}); err != nil {
		t.Fatal(err)
	}

	type confirmResult struct {
		ok  bool
		err error
	}
	results := make(chan confirmResult, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, ok, err := store.Confirm(conversationKey)
			results <- confirmResult{ok: ok, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	okCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.ok {
			okCount++
		}
	}
	if okCount != 1 {
		t.Fatalf("successful confirms = %d, want 1", okCount)
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE description = $1`, description).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("transaction count = %d, want 1", count)
	}
}

func TestStoreRecordOutboundIsIdempotentByProviderMessageID(t *testing.T) {
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
	stamp := time.Unix(0, suffix).Format("150405.000000000")
	conversationKey := "whatsmeow:test:outbound-idempotent-" + stamp + "@s.whatsapp.net"
	messageID := "wamid.outbound-idempotent." + stamp
	outbound := OutboundMessage{
		Provider:        "whatsmeow",
		SessionName:     "test",
		ConversationKey: conversationKey,
		ChatID:          "outbound-idempotent@s.whatsapp.net",
		MessageID:       messageID,
		Body:            "first reply",
	}
	if err := store.RecordOutbound(ctx, outbound); err != nil {
		t.Fatal(err)
	}
	outbound.Body = "retried reply"
	if err := store.RecordOutbound(ctx, outbound); err != nil {
		t.Fatal(err)
	}

	var count int
	var body string
	if err := store.db.QueryRow(
		`SELECT COUNT(*), COALESCE(MAX(body), '') FROM whatsapp_messages WHERE conversation_key = $1 AND provider_message_id = $2 AND direction = 'outbound'`,
		conversationKey,
		messageID,
	).Scan(&count, &body); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("outbound message count = %d, want 1", count)
	}
	if body != "retried reply" {
		t.Fatalf("outbound body = %q, want retried reply", body)
	}
}

func TestStoreConfirmsEditedSingleDraft(t *testing.T) {
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
	conversationKey := "whatsmeow:test:edit-single-" + time.Unix(0, suffix).Format("150405.000000000") + "@s.whatsapp.net"
	inbound := InboundMessage{
		Provider:        "whatsmeow",
		SessionName:     "test",
		ConversationKey: conversationKey,
		ChatID:          "628123@s.whatsapp.net",
		SenderID:        "628123@s.whatsapp.net",
		MessageID:       "wamid.edit-single." + time.Unix(0, suffix).Format("150405.000000000"),
		MessageType:     "text",
		Body:            "makan 25000 nasi padang",
	}
	if _, err := store.RecordInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}

	originalAmount := int64(25000)
	updatedAmount := int64(30000)
	description := "edited single nasi padang " + time.Unix(0, suffix).Format("150405.000000000")
	if _, err := store.Save(conversationKey, inference.ParseTextResponse{
		Intent:          "create_expense",
		Amount:          &originalAmount,
		Currency:        "IDR",
		Description:     description,
		CategoryHint:    "Makan & Minum",
		TransactionDate: "2026-04-29",
	}); err != nil {
		t.Fatal(err)
	}

	result := conversation.HandleParsed(store, conversationKey, inference.ParseTextResponse{
		Intent: "edit_draft",
		Action: "edit_draft",
		Edit: &inference.EditDraft{
			Field:  "amount",
			Amount: &updatedAmount,
		},
	}, false)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if !result.SaveDraft {
		t.Fatal("expected edited single draft to be saved")
	}
	if _, ok, err := store.Confirm(conversationKey); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("expected pending edited draft")
	}

	var amount int64
	if err := store.db.QueryRow(`SELECT amount FROM transactions WHERE description = $1 ORDER BY created_at DESC LIMIT 1`, description).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if amount != updatedAmount {
		t.Fatalf("confirmed amount = %d, want %d", amount, updatedAmount)
	}
}

func TestStoreConfirmsEditedMultiDraft(t *testing.T) {
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
	conversationKey := "whatsmeow:test:edit-multi-" + time.Unix(0, suffix).Format("150405.000000000") + "@s.whatsapp.net"
	inbound := InboundMessage{
		Provider:        "whatsmeow",
		SessionName:     "test",
		ConversationKey: conversationKey,
		ChatID:          "628123@s.whatsapp.net",
		SenderID:        "628123@s.whatsapp.net",
		MessageID:       "wamid.edit-multi." + time.Unix(0, suffix).Format("150405.000000000"),
		MessageType:     "text",
		Body:            "td ke bioskop 40k terus makan 100k",
	}
	if _, err := store.RecordInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}

	firstAmount := int64(40000)
	secondAmount := int64(100000)
	updatedSecondAmount := int64(90000)
	firstDescription := "edited multi bioskop " + time.Unix(0, suffix).Format("150405.000000000")
	secondDescription := "edited multi makan " + time.Unix(0, suffix).Format("150405.000000000")
	if _, err := store.Save(conversationKey, inference.ParseTextResponse{
		Intent: "create_multiple_transactions",
		Amount: ptrInt64ForTest(firstAmount + secondAmount),
		Transactions: []inference.TransactionDraft{
			{
				Type:            "expense",
				Amount:          firstAmount,
				Currency:        "IDR",
				Description:     firstDescription,
				CategoryHint:    "Hiburan",
				TransactionDate: "2026-04-29",
			},
			{
				Type:            "expense",
				Amount:          secondAmount,
				Currency:        "IDR",
				Description:     secondDescription,
				CategoryHint:    "Makan & Minum",
				TransactionDate: "2026-04-29",
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	targetIndex := 2
	result := conversation.HandleParsed(store, conversationKey, inference.ParseTextResponse{
		Intent: "edit_draft",
		Action: "edit_draft",
		Edit: &inference.EditDraft{
			TargetItemIndex: &targetIndex,
			Field:           "amount",
			Amount:          &updatedSecondAmount,
		},
	}, false)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if !result.SaveDraft {
		t.Fatal("expected edited multi draft to be saved")
	}
	if _, ok, err := store.Confirm(conversationKey); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("expected pending edited draft")
	}

	amounts := map[string]int64{}
	rows, err := store.db.Query(`SELECT description, amount FROM transactions WHERE description IN ($1, $2)`, firstDescription, secondDescription)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var description string
		var amount int64
		if err := rows.Scan(&description, &amount); err != nil {
			t.Fatal(err)
		}
		amounts[description] = amount
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if amounts[firstDescription] != firstAmount {
		t.Fatalf("first transaction amount = %d, want %d", amounts[firstDescription], firstAmount)
	}
	if amounts[secondDescription] != updatedSecondAmount {
		t.Fatalf("second transaction amount = %d, want %d", amounts[secondDescription], updatedSecondAmount)
	}
}

func TestStoreRunQueryTotalsAndTransactionList(t *testing.T) {
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
	stamp := time.Unix(0, suffix).Format("150405.000000000")
	senderID := "query-" + stamp + "@s.whatsapp.net"
	conversationKey := "whatsmeow:test:" + senderID
	inbound := InboundMessage{
		Provider:        "whatsmeow",
		SessionName:     "test",
		ConversationKey: conversationKey,
		ChatID:          senderID,
		SenderID:        senderID,
		MessageID:       "wamid.query." + stamp,
		MessageType:     "text",
		Body:            "query setup",
	}
	if _, err := store.RecordInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}

	expenseAmount := int64(45000)
	incomeAmount := int64(125000)
	outsideRangeAmount := int64(99000)
	expenseDescription := "query expense " + stamp
	incomeDescription := "query income " + stamp
	outsideRangeDescription := "query outside range " + stamp

	confirmParsed := func(parsed inference.ParseTextResponse) {
		t.Helper()
		if _, err := store.Save(conversationKey, parsed); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := store.Confirm(conversationKey); err != nil {
			t.Fatal(err)
		} else if !ok {
			t.Fatal("expected pending draft")
		}
	}

	confirmParsed(inference.ParseTextResponse{
		Intent:          "create_expense",
		Amount:          &expenseAmount,
		Currency:        "IDR",
		Description:     expenseDescription,
		CategoryHint:    "Makan & Minum",
		TransactionDate: "2026-04-29",
		Confidence:      0.91,
	})
	confirmParsed(inference.ParseTextResponse{
		Intent:          "create_income",
		Amount:          &incomeAmount,
		Currency:        "IDR",
		Description:     incomeDescription,
		CategoryHint:    "Income",
		TransactionDate: "2026-04-29",
		Confidence:      0.91,
	})
	confirmParsed(inference.ParseTextResponse{
		Intent:          "create_expense",
		Amount:          &outsideRangeAmount,
		Currency:        "IDR",
		Description:     outsideRangeDescription,
		CategoryHint:    "Makan & Minum",
		TransactionDate: "2026-05-01",
		Confidence:      0.91,
	})

	expenseResult, err := store.RunQuery(conversationKey, inference.QueryDraft{
		Metric: "expense_total",
		Type:   "expense",
		DateRange: inference.DateRange{
			StartDate: "2026-04-29",
			EndDate:   "2026-04-29",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if expenseResult.Total != expenseAmount {
		t.Fatalf("expense total = %d, want %d", expenseResult.Total, expenseAmount)
	}

	incomeResult, err := store.RunQuery(conversationKey, inference.QueryDraft{
		Metric: "income_total",
		Type:   "income",
		DateRange: inference.DateRange{
			StartDate: "2026-04-29",
			EndDate:   "2026-04-29",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if incomeResult.Total != incomeAmount {
		t.Fatalf("income total = %d, want %d", incomeResult.Total, incomeAmount)
	}

	listResult, err := store.RunQuery(conversationKey, inference.QueryDraft{
		Metric: "transaction_list",
		Type:   "expense",
		DateRange: inference.DateRange{
			StartDate: "2026-04-29",
			EndDate:   "2026-04-29",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if listResult.Total != expenseAmount {
		t.Fatalf("transaction list total = %d, want %d", listResult.Total, expenseAmount)
	}
	if len(listResult.Transactions) != 1 {
		t.Fatalf("transaction list length = %d, want 1", len(listResult.Transactions))
	}
	if listResult.Transactions[0].Description != expenseDescription {
		t.Fatalf("transaction list description = %q, want %q", listResult.Transactions[0].Description, expenseDescription)
	}

	allResult, err := store.RunQuery(conversationKey, inference.QueryDraft{
		Metric: "transaction_list",
		Type:   "all",
		DateRange: inference.DateRange{
			StartDate: "2026-04-29",
			EndDate:   "2026-04-29",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if allResult.Type != "all" {
		t.Fatalf("all query type = %q, want all", allResult.Type)
	}
	if allResult.Total != expenseAmount+incomeAmount {
		t.Fatalf("all transaction list total = %d, want %d", allResult.Total, expenseAmount+incomeAmount)
	}
	if len(allResult.Transactions) != 2 {
		t.Fatalf("all transaction list length = %d, want 2", len(allResult.Transactions))
	}
}

func TestReceiptUploadHashDedupeSameUser(t *testing.T) {
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
	stamp := time.Unix(0, suffix).Format("150405.000000000")
	conversationKey := "whatsmeow:test:receipt-dedupe-" + stamp + "@s.whatsapp.net"
	inbound := InboundMessage{
		Provider:        "whatsmeow",
		SessionName:     "test",
		ConversationKey: conversationKey,
		ChatID:          "receipt-dedupe@s.whatsapp.net",
		SenderID:        "receipt-dedupe@s.whatsapp.net",
		MessageID:       "wamid.receipt-dedupe." + stamp,
		MessageType:     "image",
		Body:            "",
	}
	if _, err := store.RecordInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}

	imageHash := "hash-" + stamp
	receipt, duplicate, err := store.StartReceiptProcessing(ctx, conversationKey, inbound.MessageID, imageHash, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("first receipt upload should not be duplicate")
	}
	if receipt.Status != "processing" {
		t.Fatalf("status = %q, want processing", receipt.Status)
	}

	amount := int64(50000)
	parsed := inference.ParseTextResponse{
		Intent:          "create_expense",
		Action:          "create_draft",
		Amount:          &amount,
		Currency:        "IDR",
		Description:     "receipt dedupe test " + stamp,
		CategoryHint:    "Hiburan",
		TransactionDate: "2026-04-29",
		Confidence:      0.9,
		Raw:             map[string]any{"image_hash": imageHash},
	}
	if _, err := store.Save(conversationKey, parsed); err != nil {
		t.Fatal(err)
	}

	receipt, duplicate, err = store.StartReceiptProcessing(ctx, conversationKey, "wamid.receipt-dedupe.repeat."+stamp, imageHash, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate {
		t.Fatal("second same-user receipt upload should be duplicate")
	}
	if receipt.Status != "pending_confirmation" {
		t.Fatalf("duplicate status = %q, want pending_confirmation", receipt.Status)
	}

	if _, ok, err := store.Confirm(conversationKey); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Fatal("expected pending receipt draft")
	}
	receipt, duplicate, err = store.StartReceiptProcessing(ctx, conversationKey, "wamid.receipt-dedupe.confirmed."+stamp, imageHash, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate {
		t.Fatal("confirmed same-user receipt upload should be duplicate")
	}
	if receipt.Status != "confirmed" {
		t.Fatalf("duplicate status = %q, want confirmed", receipt.Status)
	}
}

func TestReceiptUploadHashAllowsDifferentUsersAndFailedRetry(t *testing.T) {
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
	stamp := time.Unix(0, suffix).Format("150405.000000000")
	imageHash := "shared-hash-" + stamp
	firstConversation := "whatsmeow:test:receipt-user-one-" + stamp + "@s.whatsapp.net"
	secondConversation := "whatsmeow:test:receipt-user-two-" + stamp + "@s.whatsapp.net"

	for _, item := range []struct {
		conversationKey string
		senderID        string
		messageID       string
	}{
		{firstConversation, "receipt-user-one-" + stamp + "@s.whatsapp.net", "wamid.receipt-user-one." + stamp},
		{secondConversation, "receipt-user-two-" + stamp + "@s.whatsapp.net", "wamid.receipt-user-two." + stamp},
	} {
		if _, err := store.RecordInbound(ctx, InboundMessage{
			Provider:        "whatsmeow",
			SessionName:     "test",
			ConversationKey: item.conversationKey,
			ChatID:          item.senderID,
			SenderID:        item.senderID,
			MessageID:       item.messageID,
			MessageType:     "image",
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, duplicate, err := store.StartReceiptProcessing(ctx, firstConversation, "wamid.first."+stamp, imageHash, "image/jpeg"); err != nil {
		t.Fatal(err)
	} else if duplicate {
		t.Fatal("first user receipt should not be duplicate")
	}
	if err := store.MarkReceiptFailed(ctx, firstConversation, imageHash); err != nil {
		t.Fatal(err)
	}
	if receipt, duplicate, err := store.StartReceiptProcessing(ctx, firstConversation, "wamid.first.retry."+stamp, imageHash, "image/jpeg"); err != nil {
		t.Fatal(err)
	} else if duplicate {
		t.Fatal("failed receipt should be allowed to retry")
	} else if receipt.Status != "processing" {
		t.Fatalf("retry status = %q, want processing", receipt.Status)
	}
	if _, duplicate, err := store.StartReceiptProcessing(ctx, secondConversation, "wamid.second."+stamp, imageHash, "image/jpeg"); err != nil {
		t.Fatal(err)
	} else if duplicate {
		t.Fatal("same hash from different user should not be duplicate")
	}
}

func ptrInt64ForTest(value int64) *int64 {
	return &value
}
