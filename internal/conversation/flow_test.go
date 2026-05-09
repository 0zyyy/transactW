package conversation

import (
	"strings"
	"testing"
	"time"

	"transactw/internal/inference"
)

func TestHandleParsedDeletesMultiDraftItemAndPromotesSingleRemainingItem(t *testing.T) {
	store := NewStore(30 * time.Minute)
	conversationKey := "test:conversation"
	firstAmount := int64(40000)
	secondAmount := int64(100000)

	_, err := store.Save(conversationKey, inference.ParseTextResponse{
		Intent: "create_multiple_transactions",
		Amount: ptrInt64(firstAmount + secondAmount),
		Transactions: []inference.TransactionDraft{
			{
				Type:         "expense",
				Amount:       firstAmount,
				Description:  "bioskop",
				CategoryHint: "Hiburan",
				Currency:     "IDR",
			},
			{
				Type:         "expense",
				Amount:       secondAmount,
				Description:  "makan",
				CategoryHint: "Makan & Minum",
				Currency:     "IDR",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	targetIndex := 1
	result := HandleParsed(store, conversationKey, inference.ParseTextResponse{
		Intent: "edit_draft",
		Action: "edit_draft",
		Edit: &inference.EditDraft{
			TargetItemIndex: &targetIndex,
			Field:           "delete_item",
		},
	}, false)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if !result.SaveDraft {
		t.Fatal("expected edited draft to be saved")
	}

	draft, ok, err := store.Get(conversationKey)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected pending draft")
	}
	if draft.Parsed.Intent != "create_expense" {
		t.Fatalf("intent = %q, want create_expense", draft.Parsed.Intent)
	}
	if len(draft.Parsed.Transactions) != 0 {
		t.Fatalf("transactions length = %d, want promoted single item with no child transactions", len(draft.Parsed.Transactions))
	}
	if valueOrZero(draft.Parsed.Amount) != secondAmount {
		t.Fatalf("amount = %d, want %d", valueOrZero(draft.Parsed.Amount), secondAmount)
	}
	if draft.Parsed.Description != "makan" {
		t.Fatalf("description = %q, want makan", draft.Parsed.Description)
	}
	if draft.Parsed.CategoryHint != "Makan & Minum" {
		t.Fatalf("category = %q, want Makan & Minum", draft.Parsed.CategoryHint)
	}
	if !strings.Contains(result.Reply, "makan") || strings.Contains(result.Reply, "bioskop") {
		t.Fatalf("reply does not describe the remaining item cleanly: %q", result.Reply)
	}
}

func TestHandleParsedRejectsDeletingOnlyDraftItem(t *testing.T) {
	store := NewStore(30 * time.Minute)
	conversationKey := "test:conversation"
	amount := int64(25000)

	_, err := store.Save(conversationKey, inference.ParseTextResponse{
		Intent:          "create_expense",
		Amount:          &amount,
		Description:     "makan nasi padang",
		CategoryHint:    "Makan & Minum",
		Currency:        "IDR",
		TransactionDate: "2026-04-27",
	})
	if err != nil {
		t.Fatal(err)
	}

	result := HandleParsed(store, conversationKey, inference.ParseTextResponse{
		Intent: "edit_draft",
		Action: "edit_draft",
		Edit: &inference.EditDraft{
			Field: "delete_item",
		},
	}, false)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.SaveDraft {
		t.Fatal("delete-only-item edit should not save a zero-item draft")
	}

	draft, ok, err := store.Get(conversationKey)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("original draft should still exist")
	}
	if draft.Parsed.Description != "makan nasi padang" {
		t.Fatalf("description = %q, want original draft untouched", draft.Parsed.Description)
	}
}

func TestHandleParsedConfirmSingleUsesSavedReply(t *testing.T) {
	store := NewStore(30 * time.Minute)
	conversationKey := "test:conversation"
	amount := int64(25000)

	_, err := store.Save(conversationKey, inference.ParseTextResponse{
		Intent:      "create_expense",
		Amount:      &amount,
		Description: "nasi padang",
		Currency:    "IDR",
	})
	if err != nil {
		t.Fatal(err)
	}

	result := HandleParsed(store, conversationKey, inference.ParseTextResponse{Action: "confirm_draft"}, false)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Reply != "*Tersimpan*\n\nRp25.000 - nasi padang" {
		t.Fatalf("reply = %q", result.Reply)
	}
}

func TestHandleParsedConfirmMultiUsesSavedReply(t *testing.T) {
	store := NewStore(30 * time.Minute)
	conversationKey := "test:conversation"
	total := int64(140000)

	_, err := store.Save(conversationKey, inference.ParseTextResponse{
		Intent: "create_multiple_transactions",
		Amount: &total,
		Transactions: []inference.TransactionDraft{
			{Amount: 40000, Description: "bioskop"},
			{Amount: 100000, Description: "makan"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	result := HandleParsed(store, conversationKey, inference.ParseTextResponse{Action: "confirm_draft"}, false)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	for _, want := range []string{"*Tersimpan*", "2 transaksi", "*Total: Rp140.000*"} {
		if !strings.Contains(result.Reply, want) {
			t.Fatalf("reply missing %q: %q", want, result.Reply)
		}
	}
}

func TestFormatEditDraftFailureShowsExamples(t *testing.T) {
	message := formatEditDraft(inference.ParseTextResponse{}, false)

	for _, want := range []string{"yang kedua harusnya 90k", "ganti kategori transport", "catatannya jadi nasi padang"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q: %q", want, message)
		}
	}
}

func TestFormatQueryResultIsCompact(t *testing.T) {
	summary := formatQueryResult(QueryResult{
		Metric:    "expense_total",
		Type:      "expense",
		StartDate: "2026-04-29",
		EndDate:   "2026-04-29",
		Total:     45000,
	})
	if summary != "*Pengeluaran 2026-04-29*\n\nRp45.000" {
		t.Fatalf("summary = %q", summary)
	}

	list := formatQueryResult(QueryResult{
		Metric:    "transaction_list",
		Type:      "expense",
		StartDate: "2026-04-29",
		EndDate:   "2026-04-29",
		Total:     45000,
		Transactions: []QueryTransaction{
			{Amount: 45000, Description: "nasi padang", CategoryName: "Makan & Minum"},
		},
	})
	for _, want := range []string{"*Transaksi 2026-04-29*", "1. Rp45.000 - nasi padang"} {
		if !strings.Contains(list, want) {
			t.Fatalf("list missing %q: %q", want, list)
		}
	}
}

func ptrInt64(value int64) *int64 {
	return &value
}
