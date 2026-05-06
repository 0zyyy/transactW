import re
from typing import Any

from normalize import parse_int_amount, safe_float


TOTAL_LABELS = ["grand total", "total bayar", "total belanja", "jumlah", "amount due", "total"]
NEGATIVE_TOTAL_LABELS = ["subtotal", "sub total", "tax", "pajak", "ppn", "change", "kembali", "kembalian", "diskon", "discount", "cash", "tunai", "saldo"]
NON_PURCHASE_KEYWORDS = ["penarikan", "saldo", "no kartu", "terminal id", "no record", "no rekord"]
PURCHASE_KEYWORDS = ["total belanja", "total bayar", "grand total", "struk", "kasir", "konsumen", "belanja"]


def best_receipt_total(value: Any) -> tuple[int, float] | None:
    if not isinstance(value, list):
        return None
    best_amount: int | None = None
    best_confidence = 0.0
    best_score = -1.0
    for item in value:
        if not isinstance(item, dict):
            continue
        amount = parse_int_amount(item.get("amount"))
        if amount is None or amount <= 0:
            continue
        label = str(item.get("label") or "").lower()
        confidence = safe_float(item.get("confidence"), 0.5)
        score = confidence
        if any(word in label for word in TOTAL_LABELS):
            score += 0.6
        if any(word in label for word in NEGATIVE_TOTAL_LABELS):
            score -= 0.9
        if label.strip() in {"total", "grand total", "total bayar", "total belanja", "jumlah"}:
            score += 0.3
        if score > best_score:
            best_score = score
            best_amount = amount
            best_confidence = confidence
    if best_amount is None or best_score < 0.2:
        return None
    return best_amount, best_confidence


def extract_receipt_candidates(ocr_result: dict[str, Any]) -> dict[str, Any]:
    lines = normalized_ocr_lines(ocr_result)
    text = "\n".join(line["text"] for line in lines)
    lowered = text.lower()
    totals = extract_total_candidates(lines)
    has_purchase_total = any(keyword in lowered for keyword in ["total belanja", "total bayar", "grand total"])
    is_non_purchase = any(keyword in lowered for keyword in NON_PURCHASE_KEYWORDS) and not has_purchase_total
    receipt_score = 0.0
    if totals:
        receipt_score += 0.45
    if any(keyword in lowered for keyword in PURCHASE_KEYWORDS):
        receipt_score += 0.35
    if len(lines) >= 5:
        receipt_score += 0.15
    if is_non_purchase:
        receipt_score -= 0.6
    receipt_score = max(0.0, min(receipt_score, 0.95))

    return {
        "is_receipt_candidate": receipt_score >= 0.45,
        "receipt_confidence": receipt_score,
        "merchant_candidates": extract_merchant_candidates(lines),
        "date_candidates": extract_date_candidates(lines),
        "total_candidates": totals,
        "line_items": extract_line_item_candidates(lines),
        "text": text,
    }


def ocr_candidates_to_receipt_ocr(candidates: dict[str, Any], verifier: dict[str, Any] | None = None) -> dict[str, Any]:
    verifier = verifier or {}
    total = parse_int_amount(verifier.get("selected_total"))
    total_confidence = safe_float(verifier.get("confidence"), 0.0)
    totals = candidates.get("total_candidates") or []
    if total is None:
        best = best_receipt_total(totals)
        if best:
            total = best[0]
            total_confidence = best[1]
    selected_totals = []
    if total is not None:
        selected_totals.append(
            {
                "label": str(verifier.get("selected_total_label") or "total"),
                "amount": total,
                "confidence": total_confidence or safe_float(candidates.get("receipt_confidence"), 0.6),
            }
        )
    merchant = str(verifier.get("merchant") or first_or_empty(candidates.get("merchant_candidates")))
    date = str(verifier.get("date") or first_or_empty(candidates.get("date_candidates")))
    return {
        "is_receipt": bool(verifier.get("is_receipt", candidates.get("is_receipt_candidate"))),
        "receipt_confidence": safe_float(verifier.get("confidence"), safe_float(candidates.get("receipt_confidence"), 0.0)),
        "merchant": merchant,
        "date": date,
        "currency": str(verifier.get("currency") or "IDR"),
        "totals": selected_totals or totals,
        "line_items": verifier.get("line_items") if isinstance(verifier.get("line_items"), list) else candidates.get("line_items", []),
        "payment_method": str(verifier.get("payment_method") or ""),
        "notes": str(verifier.get("notes") or ""),
    }


def normalized_ocr_lines(ocr_result: dict[str, Any]) -> list[dict[str, Any]]:
    raw_lines = ocr_result.get("lines") if isinstance(ocr_result, dict) else None
    if not isinstance(raw_lines, list):
        return []
    lines: list[dict[str, Any]] = []
    for item in raw_lines:
        if not isinstance(item, dict):
            continue
        text = re.sub(r"\s+", " ", str(item.get("text") or "")).strip()
        if text:
            lines.append({"text": text, "confidence": safe_float(item.get("confidence"), 0.0)})
    return lines


def extract_total_candidates(lines: list[dict[str, Any]]) -> list[dict[str, Any]]:
    totals: list[dict[str, Any]] = []
    for index, line in enumerate(lines):
        text = line["text"]
        lowered = text.lower()
        if not any(label in lowered for label in TOTAL_LABELS + NEGATIVE_TOTAL_LABELS):
            continue
        amount = last_amount(text)
        if amount is None and index + 1 < len(lines):
            amount = last_amount(lines[index + 1]["text"])
        if amount is None:
            continue
        confidence = safe_float(line.get("confidence"), 0.5)
        if any(label in lowered for label in NEGATIVE_TOTAL_LABELS):
            confidence = min(confidence, 0.35)
        totals.append({"label": text, "amount": amount, "confidence": confidence})
    return totals


def extract_merchant_candidates(lines: list[dict[str, Any]]) -> list[str]:
    candidates: list[str] = []
    for line in lines[:5]:
        text = line["text"].strip()
        lowered = text.lower()
        if safe_float(line.get("confidence"), 0) < 0.75:
            continue
        if len(text) < 3 or any(keyword in lowered for keyword in ["tanggal", "waktu", "terminal", "jalan", "jl.", "kec."]):
            continue
        if re.search(r"\d{2}[/-]\d{2}[/-]\d{4}", lowered):
            continue
        candidates.append(text)
    return candidates[:3]


def extract_date_candidates(lines: list[dict[str, Any]]) -> list[str]:
    candidates: list[str] = []
    for line in lines:
        for day, month, year in re.findall(r"\b(\d{1,2})[/-](\d{1,2})[/-](\d{2,4})\b", line["text"]):
            normalized_year = int(year) + 2000 if len(year) == 2 else int(year)
            candidates.append(f"{normalized_year:04d}-{int(month):02d}-{int(day):02d}")
    return candidates[:3]


def extract_line_item_candidates(lines: list[dict[str, Any]]) -> list[dict[str, Any]]:
    items: list[dict[str, Any]] = []
    for line in lines:
        lowered = line["text"].lower()
        if any(keyword in lowered for keyword in TOTAL_LABELS + NEGATIVE_TOTAL_LABELS + ["ppn", "dpp", "jual", "tanggal", "waktu", "terminal", "record", "rekord", "kartu", "saldo", "telp", "npwp", "jalan", "jl.", "kec."]):
            continue
        amount = last_amount(line["text"])
        if amount is None or amount < 1000 or amount > 1_000_000:
            continue
        name = re.sub(r"(?:rp\s*)?[0-9][0-9.,]*$", "", line["text"], flags=re.IGNORECASE).strip(" :-")
        if len(name) < 2:
            continue
        if re.search(r"[/:\\]", name) or re.search(r"\b\d{1,2}[/-]\d{1,2}\b", name):
            continue
        items.append({"name": name, "amount": amount, "confidence": safe_float(line.get("confidence"), 0.5)})
    return items[:20]


def last_amount(text: str) -> int | None:
    matches = re.findall(r"(?:rp\s*)?([0-9]+(?:[.,][0-9]{3})+(?:,[0-9]{2})?|[0-9]{3,})", text, flags=re.IGNORECASE)
    if not matches:
        return None
    return parse_int_amount(matches[-1])


def first_or_empty(value: Any) -> str:
    if isinstance(value, list) and value:
        return str(value[0])
    return ""


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
    if any(word in text for word in ["bioskop", "cinema", "movie", "film", "konser", "karaoke", "netflix", "spotify", "game"]):
        return "Hiburan"
    return "Lainnya"
