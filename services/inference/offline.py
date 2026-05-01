import re
from typing import Any


def infer_intent_candidates(text: str, amount: int | None, transaction_count: int = 0, has_query: bool = False) -> list[dict[str, Any]]:
    candidates: list[dict[str, Any]] = []

    def add(intent: str, score: float, reason: str, needs_reply: bool = False) -> None:
        candidates.append(
            {
                "intent": intent,
                "score": score,
                "reason": reason,
                "needs_reply": needs_reply,
            }
        )

    if transaction_count > 1:
        add("create_multiple_transactions", 0.88, "contains multiple amount-description pairs")
    if has_query:
        if any(phrase in text for phrase in ["apa aja", "beli apa", "transaksi apa", "list", "daftar"]):
            add("query_recent_transactions", 0.88, "asks for transaction list")
        else:
            add("query_summary", 0.88, "asks for spending summary")
    if any(word in text for word in ["batal", "cancel", "ga jadi", "gajadi"]):
        add("cancel_flow", 0.95, "contains cancellation keyword")
    if re.search(r"\b(?:ya|ok|oke|simpan|confirm)\b", text) and len(text.split()) <= 3:
        add("confirm_draft", 0.9, "short confirmation keyword")
    if any(word in text for word in ["pengeluaran", "spending", "habis berapa", "minggu ini", "bulan ini"]):
        if amount is None:
            add("query_summary", 0.86, "summary period or spending query without amount")
        else:
            add("create_expense", 0.72, "contains spending word and amount")
            add("query_summary", 0.42, "contains spending query words but also has amount")
    if any(word in text for word in ["income", "gaji", "masuk", "dibayar", "freelance", "bayaran"]):
        add("create_income", 0.84 if amount else 0.68, "contains income keyword", amount is None)
    if amount is not None:
        add("create_expense", 0.78, "contains rupiah-like amount")

    if not candidates:
        add("unknown", 0.4, "no clear amount, command, or summary phrase", True)

    deduped: dict[str, dict[str, Any]] = {}
    for candidate in candidates:
        existing = deduped.get(candidate["intent"])
        if existing is None or candidate["score"] > existing["score"]:
            deduped[candidate["intent"]] = candidate

    ranked = sorted(deduped.values(), key=lambda item: item["score"], reverse=True)
    if ranked[0]["intent"] != "unknown":
        ranked.append(
            {
                "intent": "unknown",
                "score": 0.12,
                "reason": "fallback if all stronger candidates fail validation",
                "needs_reply": True,
            }
        )
    return ranked[:4]


def infer_category(text: str) -> str:
    categories = [
        ("Makan & Minum", ["makan", "kopi", "nasi", "resto", "warung", "gofood", "grabfood"]),
        ("Transport", ["gojek", "grab", "taxi", "bensin", "parkir", "tol", "transport"]),
        ("Belanja Harian", ["indomaret", "alfamart", "superindo", "snack", "sabun", "belanja"]),
        ("Tagihan", ["listrik", "internet", "pulsa", "paket data", "air", "tagihan"]),
        ("Income", ["gaji", "freelance", "income", "bayaran", "dibayar"]),
    ]
    for category, keywords in categories:
        if any(keyword in text for keyword in keywords):
            return category
    return ""


def infer_account(text: str) -> str:
    accounts = ["gopay", "ovo", "dana", "shopeepay", "bca", "mandiri", "bri", "bni", "cash", "tunai"]
    for account in accounts:
        if account in text:
            return "Cash" if account == "tunai" else account
    return ""


def cleanup_description(text: str, amount: int | None) -> str:
    description = text.strip()
    description = re.sub(r"\brp\s*[0-9][0-9\.\,]*\b", "", description, flags=re.IGNORECASE)
    description = re.sub(r"\b[0-9]+(?:[\.\,][0-9]{3})+(?:,\d{2})?\b", "", description)
    description = re.sub(r"\b[0-9]+\s*(?:rb|ribu|k)\b", "", description, flags=re.IGNORECASE)
    if amount is not None:
        description = description.replace(str(amount), "")
    return re.sub(r"\s+", " ", description).strip()
