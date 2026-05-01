package conversation

import (
	"fmt"
	"strings"

	"transactw/internal/inference"
	"transactw/internal/reply"
)

type Result struct {
	Reply     string
	SaveDraft bool
	Draft     *inference.ParseTextResponse
	Err       error
}

func HandleParsed(store DraftStore, conversationKey string, parsed inference.ParseTextResponse, debug bool) Result {
	switch parsed.Action {
	case "confirm_draft":
		draft, ok, err := store.Confirm(conversationKey)
		if err != nil {
			return Result{Err: err}
		}
		if !ok {
			return Result{Reply: "Belum ada draft yang bisa disimpan. Kirim transaksi dulu."}
		}
		return Result{Reply: formatConfirmed(draft.Parsed)}
	case "cancel_flow":
		hadDraft, err := store.Cancel(conversationKey)
		if err != nil {
			return Result{Err: err}
		}
		if hadDraft {
			return Result{Reply: "Oke, draft transaksi dibatalkan."}
		}
		return Result{Reply: "Tidak ada draft aktif untuk dibatalkan."}
	case "create_draft":
		if IsDraftIntent(parsed.Intent) {
			if _, err := store.Save(conversationKey, parsed); err != nil {
				return Result{Err: err}
			}
			return Result{
				Reply:     reply.Format(parsed, debug),
				SaveDraft: true,
				Draft:     &parsed,
			}
		}
		return Result{Reply: reply.Format(parsed, debug)}
	case "edit_draft":
		draft, ok, err := store.Get(conversationKey)
		if err != nil {
			return Result{Err: err}
		} else if !ok {
			return Result{Reply: "Belum ada draft yang bisa dikoreksi. Kirim transaksi dulu."}
		}
		updated, ok := applyEditDraft(draft.Parsed, parsed)
		if !ok {
			return Result{Reply: formatEditDraft(parsed, debug)}
		}
		if _, err := store.Save(conversationKey, updated); err != nil {
			return Result{Err: err}
		}
		return Result{Reply: "Draft diperbarui:\n\n" + reply.Format(updated, debug), SaveDraft: true, Draft: &updated}
	case "ask_clarification":
		if parsed.ClarificationPrompt != "" {
			return Result{Reply: parsed.ClarificationPrompt}
		}
		if parsed.ReplyDraft != "" {
			return Result{Reply: parsed.ReplyDraft}
		}
		return Result{Reply: reply.Format(parsed, debug)}
	case "run_query":
		if parsed.Query == nil {
			return Result{Reply: reply.Format(parsed, debug)}
		}
		if parsed.Query.NeedsClarification {
			return Result{Reply: reply.Format(parsed, debug)}
		}
		result, err := store.RunQuery(conversationKey, *parsed.Query)
		if err != nil {
			return Result{Err: err}
		}
		return Result{Reply: formatQueryResult(result)}
	case "show_help", "none", "":
		return Result{Reply: reply.Format(parsed, debug)}
	default:
		return Result{Reply: reply.Format(parsed, debug)}
	}
}

func applyEditDraft(current inference.ParseTextResponse, editParsed inference.ParseTextResponse) (inference.ParseTextResponse, bool) {
	if editParsed.Edit == nil {
		return current, false
	}
	updated := current
	field := editParsed.Edit.Field
	if field == "" || field == "unknown" {
		field = inferEditField(editParsed)
	}

	if len(updated.Transactions) > 0 {
		index := 0
		if editParsed.Edit.TargetItemIndex != nil && *editParsed.Edit.TargetItemIndex > 0 {
			index = *editParsed.Edit.TargetItemIndex - 1
		}
		if index < 0 || index >= len(updated.Transactions) {
			return current, false
		}
		if field == "delete_item" {
			updated.Transactions = append(updated.Transactions[:index], updated.Transactions[index+1:]...)
		} else if !applyEditToItem(&updated.Transactions[index], editParsed, field) {
			return current, false
		}
		recalculateTotal(&updated)
		if len(updated.Transactions) == 1 {
			updated.Intent = "create_expense"
			if updated.Transactions[0].Type == "income" {
				updated.Intent = "create_income"
			}
		}
		return updated, true
	}

	if field == "delete_item" {
		return current, false
	}
	if !applyEditToParsed(&updated, editParsed, field) {
		return current, false
	}
	return updated, true
}

func applyEditToItem(item *inference.TransactionDraft, editParsed inference.ParseTextResponse, field string) bool {
	switch field {
	case "amount":
		amount := editAmount(editParsed)
		if amount == nil || *amount <= 0 {
			return false
		}
		item.Amount = *amount
	case "category":
		category := editCategory(editParsed)
		if category == "" {
			return false
		}
		item.CategoryHint = category
	case "description":
		description := editDescription(editParsed)
		if description == "" {
			return false
		}
		item.Description = description
	default:
		return false
	}
	return true
}

func applyEditToParsed(parsed *inference.ParseTextResponse, editParsed inference.ParseTextResponse, field string) bool {
	switch field {
	case "amount":
		amount := editAmount(editParsed)
		if amount == nil || *amount <= 0 {
			return false
		}
		parsed.Amount = amount
	case "category":
		category := editCategory(editParsed)
		if category == "" {
			return false
		}
		parsed.CategoryHint = category
	case "description":
		description := editDescription(editParsed)
		if description == "" {
			return false
		}
		parsed.Description = description
	default:
		return false
	}
	return true
}

func inferEditField(parsed inference.ParseTextResponse) string {
	if parsed.Edit != nil {
		if parsed.Edit.Amount != nil || parsed.Amount != nil {
			return "amount"
		}
		if parsed.Edit.CategoryHint != "" || parsed.CategoryHint != "" {
			return "category"
		}
		if parsed.Edit.Description != "" || parsed.Description != "" {
			return "description"
		}
	}
	return "unknown"
}

func editAmount(parsed inference.ParseTextResponse) *int64 {
	if parsed.Edit != nil && parsed.Edit.Amount != nil {
		return parsed.Edit.Amount
	}
	return parsed.Amount
}

func editCategory(parsed inference.ParseTextResponse) string {
	if parsed.Edit != nil && parsed.Edit.CategoryHint != "" {
		return parsed.Edit.CategoryHint
	}
	return parsed.CategoryHint
}

func editDescription(parsed inference.ParseTextResponse) string {
	if parsed.Edit != nil && parsed.Edit.Description != "" {
		return parsed.Edit.Description
	}
	return parsed.Description
}

func recalculateTotal(parsed *inference.ParseTextResponse) {
	total := int64(0)
	for _, item := range parsed.Transactions {
		total += item.Amount
	}
	parsed.Amount = &total
}

func formatEditDraft(parsed inference.ParseTextResponse, debug bool) string {
	if debug {
		return reply.Format(parsed, true)
	}
	if parsed.Edit != nil {
		suffix := ""
		if parsed.Edit.TargetItemIndex != nil {
			suffix = fmt.Sprintf(" item %d", *parsed.Edit.TargetItemIndex)
		}
		return fmt.Sprintf("Koreksi kebaca untuk%s: %s. Belum diterapkan otomatis.", suffix, parsed.Edit.Field)
	}
	if parsed.ReplyDraft != "" {
		return parsed.ReplyDraft
	}
	return "Koreksi kebaca, tapi belum diterapkan otomatis."
}

func formatConfirmed(parsed inference.ParseTextResponse) string {
	if parsed.Intent == "create_multiple_transactions" && len(parsed.Transactions) > 0 {
		return fmt.Sprintf("Siap, %d transaksi disimpan.\nTotal: %s", len(parsed.Transactions), reply.FormatAmountIDR(valueOrZero(parsed.Amount)))
	}
	return fmt.Sprintf("Siap, transaksi disimpan.\nAmount: %s\nCatatan: %s", reply.FormatAmountIDR(valueOrZero(parsed.Amount)), parsed.Description)
}

func formatQueryResult(result QueryResult) string {
	if result.Metric == "transaction_list" {
		if len(result.Transactions) == 0 {
			return fmt.Sprintf("Belum ada transaksi untuk %s s/d %s.", result.StartDate, result.EndDate)
		}

		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("Transaksi %s s/d %s:\n", result.StartDate, result.EndDate))
		for index, tx := range result.Transactions {
			category := tx.CategoryName
			if category == "" {
				category = "Lainnya"
			}
			description := tx.Description
			if description == "" {
				description = "-"
			}
			builder.WriteString(fmt.Sprintf(
				"%d. %s - %s - %s - %s\n",
				index+1,
				tx.TransactionDate,
				category,
				description,
				reply.FormatAmountIDR(tx.Amount),
			))
		}
		return strings.TrimSpace(builder.String())
	}

	label := "Pengeluaran"
	if result.Type == "income" || result.Metric == "income_total" {
		label = "Pemasukan"
	} else if result.Type == "all" {
		label = "Total transaksi"
	}
	return fmt.Sprintf("%s %s s/d %s: %s", label, result.StartDate, result.EndDate, reply.FormatAmountIDR(result.Total))
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
