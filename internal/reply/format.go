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
		return "*Belum ada draft*\n\nKirim transaksi dulu, lalu balas *simpan* untuk menyimpan."
	case "cancel_flow":
		return "*Tidak ada draft aktif*\n\nKirim transaksi baru kalau mau mulai lagi."
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
	builder.WriteString(fmt.Sprintf("*Draft %d transaksi*\n\n", len(parsed.Transactions)))
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
	builder.WriteString("*Total: " + FormatAmountIDR(valueOrZero(parsed.Amount)) + "*\n\n")
	builder.WriteString("Balas *simpan* untuk menyimpan semua atau *batal* untuk membatalkan.")
	return builder.String()
}

func formatSingle(parsed inference.ParseTextResponse) string {
	if hasReceiptRaw(parsed) {
		return formatReceiptDraft(parsed)
	}

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
		"*Draft %s*\n\n%s\n%s\n\nKategori: %s\nTanggal: %s\n",
		kind,
		FormatAmountIDR(valueOrZero(parsed.Amount)),
		description,
		category,
		parsed.TransactionDate,
	))
	if items := inference.ReceiptItems(parsed); len(items) > 0 {
		builder.WriteString("\n*Item struk*\n")
		for index, item := range items {
			if index >= 5 {
				builder.WriteString(fmt.Sprintf("...dan %d item lain\n", len(items)-index))
				break
			}
			builder.WriteString(fmt.Sprintf("%d. %s - %s\n", item.Index, item.Name, FormatAmountIDR(item.Amount)))
		}
	}
	builder.WriteString("\nBalas *simpan* untuk menyimpan atau *batal* untuk membatalkan.")
	return builder.String()
}

func formatReceiptDraft(parsed inference.ParseTextResponse) string {
	category := parsed.CategoryHint
	if category == "" {
		category = "Lainnya"
	}
	merchant := parsed.MerchantName
	if merchant == "" {
		merchant = receiptRaw(parsed, "merchant")
	}
	date := parsed.TransactionDate
	if date == "" {
		date = "-"
	}

	var builder strings.Builder
	builder.WriteString("*Draft dari struk*\n\n")
	builder.WriteString("Total: " + FormatAmountIDR(valueOrZero(parsed.Amount)) + "\n")
	if merchant != "" {
		builder.WriteString("Merchant: " + merchant + "\n")
	}
	builder.WriteString("Tanggal: " + date + "\n")
	builder.WriteString("Kategori: " + category + "\n")

	items := inference.ReceiptItems(parsed)
	if len(items) > 0 {
		builder.WriteString("\n*Item terbaca*\n")
		for index, item := range items {
			if index >= 5 {
				builder.WriteString(fmt.Sprintf("...dan %d item lain\n", len(items)-index))
				break
			}
			builder.WriteString(fmt.Sprintf("%d. %s - %s\n", item.Index, item.Name, FormatAmountIDR(item.Amount)))
		}
	}
	builder.WriteString("\nBalas *simpan* untuk menyimpan atau *batal* untuk membatalkan.")
	return builder.String()
}

func formatQuery(parsed inference.ParseTextResponse) string {
	if parsed.Query != nil {
		if parsed.Query.NeedsClarification {
			if parsed.Query.ClarificationPrompt != "" {
				return parsed.Query.ClarificationPrompt
			}
			return "*Tanggal belum jelas*\n\nTulis tanggal atau rentang tanggalnya lagi."
		}
		return fmt.Sprintf(
			"*Aku cek dulu*\n\n%s %s s/d %s.",
			queryLabel(parsed.Query),
			parsed.Query.DateRange.StartDate,
			parsed.Query.DateRange.EndDate,
		)
	}
	return "*Aku cek dulu*\n\nSebentar ya."
}

func formatHelp() string {
	return "*Contoh yang bisa kamu kirim*\n\n" +
		"1. makan 25000 nasi padang\n" +
		"2. td bioskop 40k terus makan 100k\n" +
		"3. minggu ini habis berapa\n" +
		"4. yang kedua harusnya 90k\n\n" +
		"Balas *simpan* untuk menyimpan draft atau *batal* untuk membatalkan."
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
		return "*Transaksi belum lengkap*\n\nYang kurang: " + strings.Join(parsed.MissingFields, ", ")
	}
	if strings.TrimSpace(parsed.Intent) == "" || parsed.Intent == "unknown" {
		return "*Aku belum paham*\n\nBisa tulis lagi lebih jelas?"
	}
	return fmt.Sprintf("*Aku belum paham*\n\nIntent terbaca: `%s`.", parsed.Intent)
}

func receiptRaw(parsed inference.ParseTextResponse, key string) string {
	receiptOCR, ok := receiptOCRRaw(parsed)
	if !ok {
		return ""
	}
	value, _ := receiptOCR[key].(string)
	return strings.TrimSpace(value)
}

func hasReceiptRaw(parsed inference.ParseTextResponse) bool {
	_, ok := receiptOCRRaw(parsed)
	return ok
}

func receiptOCRRaw(parsed inference.ParseTextResponse) (map[string]any, bool) {
	if parsed.Raw == nil {
		return nil, false
	}
	receiptOCR, ok := parsed.Raw["receipt_ocr"].(map[string]any)
	return receiptOCR, ok
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
