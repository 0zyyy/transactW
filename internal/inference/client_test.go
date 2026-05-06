package inference

import "testing"

func TestSanitizeParseTextResponseDefaultsBlankFields(t *testing.T) {
	parsed := ParseTextResponse{}

	sanitizeParseTextResponse(&parsed)

	if parsed.Intent != "unknown" {
		t.Fatalf("Intent = %q, want unknown", parsed.Intent)
	}
	if parsed.Action != "none" {
		t.Fatalf("Action = %q, want none", parsed.Action)
	}
	if parsed.Currency != "IDR" {
		t.Fatalf("Currency = %q, want IDR", parsed.Currency)
	}
	if parsed.IntentCandidates == nil {
		t.Fatal("IntentCandidates should be non-nil")
	}
	if parsed.Transactions == nil {
		t.Fatal("Transactions should be non-nil")
	}
	if parsed.MissingFields == nil {
		t.Fatal("MissingFields should be non-nil")
	}
}

func TestSanitizeParseTextResponseKeepsValidFields(t *testing.T) {
	parsed := ParseTextResponse{
		Intent:   "create_expense",
		Action:   "create_draft",
		Currency: "IDR",
	}

	sanitizeParseTextResponse(&parsed)

	if parsed.Intent != "create_expense" {
		t.Fatalf("Intent = %q, want create_expense", parsed.Intent)
	}
	if parsed.Action != "create_draft" {
		t.Fatalf("Action = %q, want create_draft", parsed.Action)
	}
}
