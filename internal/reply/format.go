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
	case "help":
		return formatHelp()
	case "create_multiple_transactions":
		return formatMultiple(parsed)
	case "create_expense", "create_income":
		return formatSingle(parsed)
	case "query_summary", "query_recent_transactions":
		return formatQuery(parsed)
	case "confirm_draft":
		return "Kirim transaksi dulu, lalu balas simpan untuk menyimpan."
	case "cancel_flow":
		return "Tidak ada draft aktif untuk dibatalkan."
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
	builder.WriteString(fmt.Sprintf("Draft %d transaksi:\n", len(parsed.Transactions)))
	for index, tx := range parsed.Transactions {
		description := tx.Description
		if description == "" {
			description = "-"
		}
		builder.WriteString(fmt.Sprintf(
			"%d. %s - %s\n",
			index+1,
			FormatAmountIDR(tx.Amount),
			description,
		))
	}
	builder.WriteString("\n")
	builder.WriteString("Total: " + FormatAmountIDR(valueOrZero(parsed.Amount)) + "\n")
	builder.WriteString("Balas simpan untuk menyimpan, batal untuk membatalkan.")
	return builder.String()
}

func formatSingle(parsed inference.ParseTextResponse) string {
	kind := "pengeluaran"
	if parsed.Intent == "create_income" {
		kind = "pemasukan"
	}
	category := parsed.CategoryHint
	if category == "" {
		category = "Lainnya"
	}
	description := parsed.Description
	if description == "" {
		description = "-"
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf(
		"Draft %s:\n%s - %s\nKategori: %s\nTanggal: %s\n",
		kind,
		FormatAmountIDR(valueOrZero(parsed.Amount)),
		description,
		category,
		parsed.TransactionDate,
	))
	if items := inference.ReceiptItems(parsed); len(items) > 0 {
		builder.WriteString("\nItem struk:\n")
		for index, item := range items {
			if index >= 5 {
				builder.WriteString(fmt.Sprintf("...dan %d item lain\n", len(items)-index))
				break
			}
			builder.WriteString(fmt.Sprintf("%d. %s - %s\n", item.Index, item.Name, FormatAmountIDR(item.Amount)))
		}
	}
	builder.WriteString("\nBalas simpan untuk menyimpan, batal untuk membatalkan.")
	return builder.String()
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
			"Aku cek %s %s s/d %s.",
			queryLabel(parsed.Query),
			parsed.Query.DateRange.StartDate,
			parsed.Query.DateRange.EndDate,
		)
	}
	return "Aku cek datanya dulu ya."
}

func formatHelp() string {
	return "Contoh:\n" +
		"makan 25000 nasi padang\n" +
		"td bioskop 40k terus makan 100k\n" +
		"minggu ini habis berapa\n" +
		"yang kedua harusnya 90k\n" +
		"simpan\n" +
		"batal"
}

func queryLabel(query *inference.QueryDraft) string {
	if query == nil {
		return "transaksi"
	}
	if query.Metric == "transaction_list" {
		return "daftar transaksi"
	}
	if query.Type == "income" || query.Metric == "income_total" {
		return "pemasukan"
	}
	if query.Type == "all" {
		return "semua transaksi"
	}
	return "pengeluaran"
}

func formatUnknown(parsed inference.ParseTextResponse) string {
	if len(parsed.MissingFields) > 0 {
		return "Aku belum bisa baca transaksi ini. Field yang kurang: " + strings.Join(parsed.MissingFields, ", ")
	}
	if strings.TrimSpace(parsed.Intent) == "" || parsed.Intent == "unknown" {
		return "Aku belum yakin maksudnya apa. Bisa tulis lagi lebih jelas?"
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
