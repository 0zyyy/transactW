package reply

import (
	"encoding/json"
	"fmt"
	"strings"

	"transactw/internal/inference"
)

func Format(parsed inference.ParseTextResponse, debug bool) string {
	if debug {
		body, err := json.MarshalIndent(parsed, "", "  ")
		if err != nil {
			return "parse result could not be formatted as JSON"
		}
		return string(body)
	}

	switch parsed.Intent {
	case "create_multiple_transactions":
		return formatMultiple(parsed)
	case "create_expense", "create_income":
		return formatSingle(parsed)
	case "query_summary", "query_recent_transactions":
		return formatQuery(parsed)
	case "confirm_draft":
		return "Siap, ini kebaca sebagai konfirmasi draft."
	case "cancel_flow":
		return "Siap, draft dibatalkan."
	default:
		return formatUnknown(parsed)
	}
}

func ShouldDebug(raw string, defaultDebug bool) bool {
	trimmed := strings.TrimSpace(strings.ToLower(raw))
	return defaultDebug || trimmed == "debug" || strings.HasPrefix(trimmed, "debug ")
}

func StripDebugPrefix(raw string) string {
	trimmed := strings.TrimSpace(raw)
	lowered := strings.ToLower(trimmed)
	if lowered == "debug" {
		return ""
	}
	if strings.HasPrefix(lowered, "debug ") {
		return strings.TrimSpace(trimmed[len("debug "):])
	}
	return raw
}

func formatMultiple(parsed inference.ParseTextResponse) string {
	if len(parsed.Transactions) == 0 {
		return formatSingle(parsed)
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Aku nemu %d transaksi:\n", len(parsed.Transactions)))
	for index, tx := range parsed.Transactions {
		category := tx.CategoryHint
		if category == "" {
			category = "Lainnya"
		}
		description := tx.Description
		if description == "" {
			description = "-"
		}
		builder.WriteString(fmt.Sprintf(
			"%d. %s - %s - %s\n",
			index+1,
			category,
			description,
			FormatAmountIDR(tx.Amount),
		))
	}
	builder.WriteString("\n")
	builder.WriteString("Total: " + FormatAmountIDR(valueOrZero(parsed.Amount)) + "\n")
	builder.WriteString("Balas `simpan` untuk lanjut, atau `debug ...` untuk lihat JSON.")
	return builder.String()
}

func formatSingle(parsed inference.ParseTextResponse) string {
	kind := "Expense"
	if parsed.Intent == "create_income" {
		kind = "Income"
	}
	category := parsed.CategoryHint
	if category == "" {
		category = "Lainnya"
	}
	description := parsed.Description
	if description == "" {
		description = "-"
	}

	return fmt.Sprintf(
		"%s kebaca:\nAmount: %s\nKategori: %s\nCatatan: %s\nTanggal: %s\nConfidence: %.2f\n\nBalas `simpan` untuk lanjut, atau `debug ...` untuk lihat JSON.",
		kind,
		FormatAmountIDR(valueOrZero(parsed.Amount)),
		category,
		description,
		parsed.TransactionDate,
		parsed.Confidence,
	)
}

func formatQuery(parsed inference.ParseTextResponse) string {
	if parsed.Query != nil {
		if parsed.Query.NeedsClarification {
			if parsed.Query.ClarificationPrompt != "" {
				return parsed.Query.ClarificationPrompt
			}
			return "Aku belum yakin tanggalnya. Tulis tanggal atau rentang tanggalnya lagi."
		}
		return fmt.Sprintf(
			"Ini kebaca sebagai `%s`.\nMetric: %s\nTipe: %s\nTanggal: %s s/d %s\nConfidence tanggal: %.2f\n\nNanti ini akan query backend transaksi.",
			parsed.Intent,
			parsed.Query.Metric,
			parsed.Query.Type,
			parsed.Query.DateRange.StartDate,
			parsed.Query.DateRange.EndDate,
			parsed.Query.DateRange.Confidence,
		)
	}
	return fmt.Sprintf(
		"Ini kebaca sebagai `%s`.\nNanti ini akan query backend transaksi.\nConfidence: %.2f\n\nBalas `debug ...` untuk lihat JSON.",
		parsed.Intent,
		parsed.Confidence,
	)
}

func formatUnknown(parsed inference.ParseTextResponse) string {
	if len(parsed.MissingFields) > 0 {
		return "Aku belum bisa baca transaksi ini. Field yang kurang: " + strings.Join(parsed.MissingFields, ", ")
	}
	return fmt.Sprintf("Aku belum yakin maksudnya apa. Intent terbaca: `%s`.", parsed.Intent)
}

func FormatAmountIDR(amount int64) string {
	sign := ""
	if amount < 0 {
		sign = "-"
		amount = -amount
	}

	raw := fmt.Sprintf("%d", amount)
	var parts []string
	for len(raw) > 3 {
		parts = append([]string{raw[len(raw)-3:]}, parts...)
		raw = raw[:len(raw)-3]
	}
	parts = append([]string{raw}, parts...)
	return sign + "Rp" + strings.Join(parts, ".")
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
