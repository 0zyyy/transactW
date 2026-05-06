package inference

import "testing"

func TestReceiptItemsReadsNormalizedReceiptOCR(t *testing.T) {
	parsed := ParseTextResponse{
		Raw: map[string]any{
			"receipt_ocr": map[string]any{
				"line_items": []any{
					map[string]any{"name": "Tiket Bioskop", "amount": float64(50000), "confidence": 0.82},
				},
			},
		},
	}

	items := ReceiptItems(parsed)
	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	if items[0].Name != "Tiket Bioskop" {
		t.Fatalf("item name = %q, want Tiket Bioskop", items[0].Name)
	}
	if items[0].Amount != 50000 {
		t.Fatalf("item amount = %d, want 50000", items[0].Amount)
	}
}

func TestReceiptItemsFallsBackToReceiptCandidates(t *testing.T) {
	parsed := ParseTextResponse{
		Raw: map[string]any{
			"receipt_candidates": map[string]any{
				"line_items": []any{
					map[string]any{"name": "Popcorn", "amount": float64(35000), "confidence": 0.75},
				},
			},
		},
	}

	items := ReceiptItems(parsed)
	if len(items) != 1 {
		t.Fatalf("item count = %d, want 1", len(items))
	}
	if items[0].Name != "Popcorn" {
		t.Fatalf("item name = %q, want Popcorn", items[0].Name)
	}
	if items[0].Amount != 35000 {
		t.Fatalf("item amount = %d, want 35000", items[0].Amount)
	}
}
