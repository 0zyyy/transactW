//go:build whatsmeow

package main

import (
	"transactw/internal/conversation"
	"transactw/internal/inference"
)

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
