package conversation

import (
	"fmt"

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
	switch parsed.Intent {
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
	default:
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
	}
}

func formatConfirmed(parsed inference.ParseTextResponse) string {
	if parsed.Intent == "create_multiple_transactions" && len(parsed.Transactions) > 0 {
		return fmt.Sprintf("Siap, %d transaksi dikonfirmasi sementara.\nTotal: %s", len(parsed.Transactions), reply.FormatAmountIDR(valueOrZero(parsed.Amount)))
	}
	return fmt.Sprintf("Siap, transaksi dikonfirmasi sementara.\nAmount: %s\nCatatan: %s", reply.FormatAmountIDR(valueOrZero(parsed.Amount)), parsed.Description)
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
