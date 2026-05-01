import re
from typing import Any

from normalize import parse_int_amount, safe_float


def best_receipt_total(value: Any) -> tuple[int, float] | None:
    if not isinstance(value, list):
        return None
    best_amount: int | None = None
    best_confidence = 0.0
    best_score = -1.0
    positive_words = ["grand", "total bayar", "total payment", "amount due", "jumlah", "dibayar", "paid", "total"]
    negative_words = ["subtotal", "sub total", "tax", "pajak", "ppn", "change", "kembalian", "diskon", "discount", "cash", "tunai"]
    for item in value:
        if not isinstance(item, dict):
            continue
        amount = parse_int_amount(item.get("amount"))
        if amount is None or amount <= 0:
            continue
        label = str(item.get("label") or "").lower()
        confidence = safe_float(item.get("confidence"), 0.5)
        score = confidence
        if any(word in label for word in positive_words):
            score += 0.6
        if any(word in label for word in negative_words):
            score -= 0.9
        if label.strip() in {"total", "grand total", "total bayar", "jumlah"}:
            score += 0.3
        if score > best_score:
            best_score = score
            best_amount = amount
            best_confidence = confidence
    if best_amount is None or best_score < 0.2:
        return None
    return best_amount, best_confidence


def normalize_receipt_date(value: Any, fallback: str) -> str:
    raw = str(value or "").strip()
    if re.fullmatch(r"\d{4}-\d{2}-\d{2}", raw):
        return raw
    return fallback


def receipt_description(ocr: dict[str, Any], caption: str) -> str:
    merchant = str(ocr.get("merchant") or "").strip()
    if merchant:
        return merchant
    if caption.strip():
        return caption.strip()
    return "struk"


def infer_receipt_category(ocr: dict[str, Any]) -> str:
    text = " ".join(
        [
            str(ocr.get("merchant") or ""),
            str(ocr.get("notes") or ""),
            " ".join(str(item.get("name") or "") for item in ocr.get("line_items") or [] if isinstance(item, dict)),
        ]
    ).lower()
    if any(word in text for word in ["resto", "restaurant", "cafe", "kopi", "bakery", "food", "makan", "ayam", "nasi", "martabak"]):
        return "Makan & Minum"
    if any(word in text for word in ["mart", "indomaret", "alfamart", "supermarket", "grocery"]):
        return "Belanja Harian"
    if any(word in text for word in ["grab", "gojek", "taxi", "parkir", "tol", "pertamina", "shell", "spbu"]):
        return "Transport"
    return "Lainnya"
