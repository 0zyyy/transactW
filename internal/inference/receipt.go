package inference

type ReceiptItem struct {
	Index      int     `json:"index"`
	Name       string  `json:"name"`
	Amount     int64   `json:"amount"`
	Confidence float64 `json:"confidence"`
}

func ReceiptItems(parsed ParseTextResponse) []ReceiptItem {
	receiptOCR, ok := parsed.Raw["receipt_ocr"].(map[string]any)
	if !ok {
		receiptOCR, ok = parsed.Raw["receipt_candidates"].(map[string]any)
		if !ok {
			return nil
		}
	}
	lineItems, ok := receiptOCR["line_items"].([]any)
	if !ok {
		return nil
	}

	items := make([]ReceiptItem, 0, len(lineItems))
	for _, rawItem := range lineItems {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		name, _ := item["name"].(string)
		amount := int64FromAny(item["amount"])
		if name == "" || amount <= 0 {
			continue
		}
		items = append(items, ReceiptItem{
			Index:      len(items) + 1,
			Name:       name,
			Amount:     amount,
			Confidence: float64FromAny(item["confidence"]),
		})
	}
	return items
}

func int64FromAny(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case float32:
		return int64(typed)
	default:
		return 0
	}
}

func float64FromAny(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}
