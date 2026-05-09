package reply

import (
	"strings"
	"testing"

	"transactw/internal/inference"
)

func TestFormatUnknownBlankIntentIsFriendly(t *testing.T) {
	message := Format(inference.ParseTextResponse{}, false)

	if strings.Contains(message, "``") {
		t.Fatalf("message should not expose blank intent: %q", message)
	}
	if message != "*Aku belum paham*\n\nBisa tulis lagi lebih jelas?" {
		t.Fatalf("message = %q", message)
	}
}

func TestFormatUnknownIntentIsFriendly(t *testing.T) {
	message := Format(inference.ParseTextResponse{Intent: "unknown", Action: "none"}, false)

	if strings.Contains(message, "Intent terbaca") {
		t.Fatalf("message should not expose unknown intent: %q", message)
	}
}

func TestFormatSingleDraftIsCompact(t *testing.T) {
	amount := int64(25000)
	message := Format(inference.ParseTextResponse{
		Intent:          "create_expense",
		Amount:          &amount,
		Description:     "nasi padang",
		CategoryHint:    "Makan & Minum",
		TransactionDate: "2026-04-27",
		Confidence:      0.91,
	}, false)

	for _, debugText := range []string{"Confidence", "Amount:", "debug"} {
		if strings.Contains(message, debugText) {
			t.Fatalf("message should not expose %q: %q", debugText, message)
		}
	}
	for _, want := range []string{"*Draft pengeluaran*", "Rp25.000", "nasi padang", "Balas *simpan*"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q: %q", want, message)
		}
	}
}

func TestFormatMultipleDraftIsCompact(t *testing.T) {
	total := int64(140000)
	message := Format(inference.ParseTextResponse{
		Intent: "create_multiple_transactions",
		Amount: &total,
		Transactions: []inference.TransactionDraft{
			{Amount: 40000, Description: "bioskop"},
			{Amount: 100000, Description: "makan"},
		},
	}, false)

	for _, want := range []string{"*Draft 2 transaksi*", "1. Rp40.000 - bioskop", "2. Rp100.000 - makan", "*Total: Rp140.000*"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q: %q", want, message)
		}
	}
	if strings.Contains(message, "debug") || strings.Contains(message, "Confidence") {
		t.Fatalf("message should not expose debug wording: %q", message)
	}
}

func TestFormatHelpIncludesExamples(t *testing.T) {
	message := Format(inference.ParseTextResponse{Intent: "help", Action: "show_help"}, false)

	for _, want := range []string{"makan 25000 nasi padang", "minggu ini habis berapa", "yang kedua harusnya 90k", "simpan", "batal"} {
		if !strings.Contains(message, want) {
			t.Fatalf("help message missing %q: %q", want, message)
		}
	}
}
