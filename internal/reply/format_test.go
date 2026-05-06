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
	if message != "Aku belum yakin maksudnya apa. Bisa tulis lagi lebih jelas?" {
		t.Fatalf("message = %q", message)
	}
}

func TestFormatUnknownIntentIsFriendly(t *testing.T) {
	message := Format(inference.ParseTextResponse{Intent: "unknown", Action: "none"}, false)

	if strings.Contains(message, "Intent terbaca") {
		t.Fatalf("message should not expose unknown intent: %q", message)
	}
}
