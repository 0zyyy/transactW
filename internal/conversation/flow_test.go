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

func ptrInt64(value int64) *int64 {
	return &value
}
