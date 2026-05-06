#!/usr/bin/env python3
import json
import re
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from config import (
    GEMINI_API_KEY,
    GEMINI_MODEL,
    GEMINI_TIMEOUT_SECONDS,
    LOCAL_CONFIDENCE_THRESHOLD,
    OCR_ALLOW_GEMINI_VISION_FALLBACK,
    OCR_ENGINE,
    OCR_GEMINI_VERIFY,
    OCR_TOTAL_DRAFT_CONFIDENCE_THRESHOLD,
    PARSER_VERSION,
    PORT,
)
from dates import date_range, indonesian_months, normalize_date_range as normalize_date_range_for_today
from gemini import extract_gemini_text, generate_content, strip_json_fence
from normalize import normalize_category, parse_int_amount, safe_float
from ocr import OCRError, extract_text_with_doctr
from offline import cleanup_description, infer_account, infer_category, infer_intent_candidates
from prompts import build_prompt, build_receipt_ocr_prompt
from receipt import best_receipt_total, extract_receipt_candidates, infer_receipt_category, normalize_receipt_date, ocr_candidates_to_receipt_ocr, receipt_description
from receipt_verify import verify_receipt_with_gemini


def normalize_date_range(text: str) -> dict[str, Any]:
    return normalize_date_range_for_today(text, datetime.now().date())


class Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        if self.path == "/healthz":
            self.write_json(
                200,
                {
                    "status": "ok",
                    "gemini_enabled": bool(GEMINI_API_KEY),
                    "parser_version": PARSER_VERSION,
                },
            )
            return
        self.write_json(404, {"error": "not found"})

    def do_POST(self) -> None:
        if self.path not in {"/v1/parse/text", "/v1/parse/receipt"}:
            self.write_json(404, {"error": "not found"})
            return

        try:
            request = self.read_json()
            if self.path == "/v1/parse/receipt":
                image_base64 = str(request.get("image_base64") or "").strip()
                mime_type = str(request.get("mime_type") or "image/jpeg").strip()
                caption = str(request.get("caption") or "").strip()
                if not image_base64:
                    self.write_json(400, {"error": "image_base64 is required"})
                    return
                parsed = route_parse_receipt(image_base64, mime_type, caption, request.get("conversation"))
                self.write_json(200, parsed)
                return

            text = str(request.get("text") or "").strip()
            if not text:
                self.write_json(400, {"error": "text is required"})
                return

            parsed = route_parse(text, request.get("conversation"))

            self.write_json(200, parsed)
        except Exception as exc:
            self.write_json(500, {"error": str(exc)})

    def read_json(self) -> dict[str, Any]:
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        if not body:
            return {}
        return json.loads(body)

    def write_json(self, status: int, payload: dict[str, Any]) -> None:
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, format: str, *args: Any) -> None:
        return


def parse_with_gemini(text: str, conversation: Any = None) -> dict[str, Any]:
    prompt = build_prompt(text, conversation)
    schema = {
        "type": "object",
        "properties": {
            "intent": {"type": "string"},
            "action": {"type": "string"},
            "reply_draft": {"type": "string"},
            "needs_confirmation": {"type": "boolean"},
            "needs_clarification": {"type": "boolean"},
            "clarification_prompt": {"type": "string"},
            "intent_candidates": {
                "type": "array",
                "items": {
                    "type": "object",
                    "properties": {
                        "intent": {"type": "string"},
                        "score": {"type": "number"},
                        "reason": {"type": "string"},
                        "needs_reply": {"type": "boolean"},
                    },
                    "required": ["intent", "score", "reason", "needs_reply"],
                },
            },
            "amount": {"type": "integer"},
            "currency": {"type": "string"},
            "merchant_name": {"type": "string"},
            "description": {"type": "string"},
            "category_hint": {"type": "string"},
            "account_hint": {"type": "string"},
            "transaction_date": {"type": "string"},
            "query": {
                "type": "object",
                "properties": {
                    "metric": {"type": "string"},
                    "type": {"type": "string"},
                    "needs_clarification": {"type": "boolean"},
                    "clarification_prompt": {"type": "string"},
                    "date_range": {
                        "type": "object",
                        "properties": {
                            "raw_text": {"type": "string"},
                            "preset": {"type": "string"},
                            "start_date": {"type": "string"},
                            "end_date": {"type": "string"},
                            "confidence": {"type": "number"},
                        },
                        "required": ["raw_text", "preset", "start_date", "end_date", "confidence"],
                    },
                },
                "required": ["metric", "type", "date_range", "needs_clarification", "clarification_prompt"],
            },
            "edit": {
                "type": "object",
                "properties": {
                    "target_item_index": {"type": "integer"},
                    "field": {"type": "string"},
                    "value": {"type": "string"},
                    "amount": {"type": "integer"},
                    "category_hint": {"type": "string"},
                    "description": {"type": "string"},
                },
            },
            "transactions": {
                "type": "array",
                "items": {
                    "type": "object",
                    "properties": {
                        "type": {"type": "string"},
                        "amount": {"type": "integer"},
                        "currency": {"type": "string"},
                        "merchant_name": {"type": "string"},
                        "description": {"type": "string"},
                        "category_hint": {"type": "string"},
                        "account_hint": {"type": "string"},
                        "transaction_date": {"type": "string"},
                    },
                    "required": [
                        "type",
                        "amount",
                        "currency",
                        "merchant_name",
                        "description",
                        "category_hint",
                        "account_hint",
                        "transaction_date",
                    ],
                },
            },
            "confidence": {"type": "number"},
            "missing_fields": {"type": "array", "items": {"type": "string"}},
        },
        "required": [
            "intent",
            "action",
            "reply_draft",
            "needs_confirmation",
            "needs_clarification",
            "clarification_prompt",
            "intent_candidates",
            "currency",
            "merchant_name",
            "description",
            "category_hint",
            "account_hint",
            "transaction_date",
            "query",
            "transactions",
            "confidence",
            "missing_fields",
        ],
    }
    payload = {
        "contents": [{"parts": [{"text": prompt}]}],
        "generationConfig": {
            "temperature": 0.1,
            "responseMimeType": "application/json",
            "responseSchema": schema,
        },
    }
    data = generate_content(payload)
    text_response = extract_gemini_text(data)
    parsed = json.loads(strip_json_fence(text_response))
    parsed["_source_text"] = text
    return normalize_parse(parsed)


def parse_receipt_with_gemini(image_base64: str, mime_type: str, caption: str, conversation: Any = None) -> dict[str, Any]:
    prompt = build_receipt_ocr_prompt(caption)
    payload = {
        "contents": [
            {
                "parts": [
                    {"text": prompt},
                    {"inline_data": {"mime_type": mime_type or "image/jpeg", "data": image_base64}},
                ]
            }
        ],
        "generationConfig": {
            "temperature": 0.1,
            "responseMimeType": "application/json",
        },
    }
    data = generate_content(payload)
    return json.loads(strip_json_fence(extract_gemini_text(data)))


def route_parse(text: str, conversation: Any = None) -> dict[str, Any]:
    normalized_text = normalize_chat_typos(text)
    shortcut = parse_short_command(normalized_text)
    if shortcut is not None:
        shortcut["raw"] = {
            "provider": "local_shortcut",
            "gemini_called": False,
            "fallback_reason": "short_command",
            "original_text": text,
            "normalized_text": normalized_text,
            "parser_version": PARSER_VERSION,
        }
        return shortcut

    if GEMINI_API_KEY:
        try:
            gemini = parse_with_gemini(normalized_text, conversation)
            gemini["raw"] = {
                "provider": "gemini",
                "model": GEMINI_MODEL,
                "gemini_called": True,
                "fallback_reason": "natural_language_default",
                "original_text": text,
                "normalized_text": normalized_text,
                "parser_version": PARSER_VERSION,
            }
            return gemini
        except Exception as exc:
            local = parse_with_offline_fallback(normalized_text)
            local["raw"] = {
                "provider": "offline_fallback_after_gemini_error",
                "gemini_called": True,
                "fallback_reason": "gemini_error",
                "gemini_error": str(exc),
                "local_confidence": local.get("confidence", 0),
                "original_text": text,
                "normalized_text": normalized_text,
                "parser_version": PARSER_VERSION,
            }
            return local

    local = parse_with_offline_fallback(normalized_text)
    unknown_month_phrase = detect_unknown_month_date_phrase(normalized_text)
    if unknown_month_phrase is not None:
        clarified = unknown_month_date_clarification(normalized_text)
        clarified["raw"] = {
            "provider": "offline_fallback_clarification",
            "gemini_called": False,
            "fallback_reason": "gemini_disabled_unknown_month_date_phrase",
            "unknown_phrase": unknown_month_phrase,
            "original_text": text,
            "normalized_text": normalized_text,
            "parser_version": PARSER_VERSION,
        }
        return clarified
    decision = local_route_decision(text, local)
    if not decision["use_local"] and not GEMINI_API_KEY and looks_like_messy_query(normalized_text):
        local = messy_query_clarification(normalized_text)
        local["raw"] = {
            "provider": "offline_fallback_clarification",
            "gemini_called": False,
            "fallback_reason": "gemini_disabled_messy_query",
            "original_text": text,
            "normalized_text": normalized_text,
            "parser_version": PARSER_VERSION,
        }
        return local
    if decision["use_local"] or not GEMINI_API_KEY:
        local["raw"] = {
            "provider": "offline_fallback_rules",
            "gemini_called": False,
            "fallback_reason": decision["reason"] if decision["use_local"] else "gemini_disabled",
            "local_confidence": local.get("confidence", 0),
            "original_text": text,
            "normalized_text": normalized_text,
            "parser_version": PARSER_VERSION,
        }
        return local


def route_parse_receipt(image_base64: str, mime_type: str, caption: str, conversation: Any = None) -> dict[str, Any]:
    if OCR_ENGINE != "doctr":
        return route_parse_receipt_with_gemini_vision(image_base64, mime_type, caption, "ocr_engine_not_doctr")

    try:
        ocr_result = extract_text_with_doctr(image_base64)
    except OCRError as exc:
        if OCR_ALLOW_GEMINI_VISION_FALLBACK:
            return route_parse_receipt_with_gemini_vision(image_base64, mime_type, caption, f"doctr_error:{exc}")
        return receipt_ocr_clarification(caption, f"doctr_error:{exc}")

    candidates = extract_receipt_candidates(ocr_result)
    if not candidates.get("is_receipt_candidate"):
        ocr = ocr_candidates_to_receipt_ocr(candidates)
        parsed = normalize_receipt_ocr(ocr, caption)
        parsed["raw"] = receipt_raw("doctr", caption, ocr_result, candidates, None, ocr, "not_receipt_candidate")
        return parsed

    verifier = None
    if OCR_GEMINI_VERIFY and GEMINI_API_KEY:
        try:
            verifier = verify_receipt_with_gemini(ocr_result, candidates)
        except Exception as exc:
            verifier = {"error": str(exc)}

    ocr = ocr_candidates_to_receipt_ocr(candidates, verifier if verifier and "error" not in verifier else None)
    parsed = normalize_receipt_ocr(ocr, caption)
    provider = "doctr_gemini_verifier" if verifier else "doctr"
    parsed["raw"] = receipt_raw(provider, caption, ocr_result, candidates, verifier, ocr, "receipt_ocr")
    return parsed


def route_parse_receipt_with_gemini_vision(image_base64: str, mime_type: str, caption: str, reason: str) -> dict[str, Any]:
    if not GEMINI_API_KEY:
        return receipt_ocr_clarification(caption, reason)
    ocr = parse_receipt_with_gemini(image_base64, mime_type, caption)
    parsed = normalize_receipt_ocr(ocr, caption)
    parsed["raw"] = {
        "provider": "gemini_vision",
        "model": GEMINI_MODEL,
        "gemini_called": True,
        "fallback_reason": reason,
        "original_text": caption,
        "normalized_text": caption,
        "receipt_ocr": ocr,
        "parser_version": PARSER_VERSION,
    }
    return parsed


def receipt_ocr_clarification(caption: str, reason: str) -> dict[str, Any]:
    parsed = normalize_parse(
        {
            "intent": "unknown",
            "action": "ask_clarification",
            "reply_draft": "OCR struk lagi bermasalah.",
            "needs_confirmation": False,
            "needs_clarification": True,
            "clarification_prompt": "OCR struk lagi bermasalah. Kirim foto yang lebih jelas atau tulis totalnya sebagai teks dulu ya.",
            "intent_candidates": [
                {
                    "intent": "create_expense",
                    "score": 0.4,
                    "reason": "receipt image received but OCR failed",
                    "needs_reply": True,
                }
            ],
            "currency": "IDR",
            "merchant_name": "",
            "description": caption,
            "category_hint": "",
            "account_hint": "",
            "transaction_date": datetime.now().date().isoformat(),
            "transactions": [],
            "confidence": 0.0,
            "missing_fields": ["amount"],
        }
    )
    parsed["raw"] = {
        "provider": "receipt_ocr_failed",
        "gemini_called": False,
        "fallback_reason": reason,
        "original_text": caption,
        "normalized_text": caption,
        "parser_version": PARSER_VERSION,
    }
    return parsed


def receipt_raw(
    provider: str,
    caption: str,
    ocr_result: dict[str, Any],
    candidates: dict[str, Any],
    verifier: dict[str, Any] | None,
    normalized_ocr: dict[str, Any],
    reason: str,
) -> dict[str, Any]:
    return {
        "provider": provider,
        "model": GEMINI_MODEL if verifier else "",
        "gemini_called": bool(verifier),
        "fallback_reason": reason,
        "original_text": caption,
        "normalized_text": caption,
        "receipt_ocr": normalized_ocr,
        "receipt_doctr": ocr_result,
        "receipt_candidates": candidates,
        "gemini_verifier": verifier or {},
        "parser_version": PARSER_VERSION,
    }


def normalize_receipt_ocr(ocr: dict[str, Any], caption: str) -> dict[str, Any]:
    today = datetime.now().date().isoformat()
    if not isinstance(ocr, dict) or not bool(ocr.get("is_receipt")):
        return normalize_parse(
            {
                "intent": "unknown",
                "action": "none",
                "reply_draft": "",
                "needs_confirmation": False,
                "needs_clarification": False,
                "clarification_prompt": "",
                "intent_candidates": [],
                "currency": "IDR",
                "merchant_name": "",
                "description": caption,
                "category_hint": "",
                "account_hint": "",
                "transaction_date": today,
                "transactions": [],
                "confidence": safe_float(ocr.get("receipt_confidence"), 0) if isinstance(ocr, dict) else 0,
                "missing_fields": [],
            }
        )

    total = best_receipt_total(ocr.get("totals"))
    if total is None:
        return normalize_parse(
            {
                "intent": "unknown",
                "action": "ask_clarification",
                "reply_draft": "Struk kebaca, tapi totalnya belum jelas.",
                "needs_confirmation": False,
                "needs_clarification": True,
                "clarification_prompt": "Struknya kebaca, tapi totalnya belum jelas. Bisa kirim foto yang lebih terang atau tulis totalnya?",
                "intent_candidates": [
                    {
                        "intent": "create_expense",
                        "score": 0.5,
                        "reason": "receipt detected but total is missing or unreadable",
                        "needs_reply": True,
                    }
                ],
                "currency": str(ocr.get("currency") or "IDR"),
                "merchant_name": str(ocr.get("merchant") or ""),
                "description": receipt_description(ocr, caption),
                "category_hint": infer_receipt_category(ocr),
                "account_hint": "",
                "transaction_date": normalize_receipt_date(ocr.get("date"), today),
                "transactions": [],
                "confidence": min(safe_float(ocr.get("receipt_confidence"), 0.5), 0.6),
                "missing_fields": ["amount"],
            }
        )

    amount, total_confidence = total
    receipt_confidence = safe_float(ocr.get("receipt_confidence"), 0.7)
    confidence = min(max((receipt_confidence + total_confidence) / 2, total_confidence), 0.95)
    if total_confidence < OCR_TOTAL_DRAFT_CONFIDENCE_THRESHOLD:
        return normalize_parse(
            {
                "intent": "unknown",
                "action": "ask_clarification",
                "reply_draft": "Struk kebaca, tapi totalnya belum cukup yakin.",
                "needs_confirmation": False,
                "needs_clarification": True,
                "clarification_prompt": "Struknya kebaca, tapi totalnya belum cukup yakin. Tolong balas total yang benar ya.",
                "intent_candidates": [
                    {
                        "intent": "create_expense",
                        "score": total_confidence,
                        "reason": "receipt total detected with low confidence",
                        "needs_reply": True,
                    }
                ],
                "amount": amount,
                "currency": str(ocr.get("currency") or "IDR"),
                "merchant_name": str(ocr.get("merchant") or ""),
                "description": receipt_description(ocr, caption),
                "category_hint": infer_receipt_category(ocr),
                "account_hint": "",
                "transaction_date": normalize_receipt_date(ocr.get("date"), today),
                "transactions": [],
                "confidence": confidence,
                "missing_fields": [],
            }
        )
    return normalize_parse(
        {
            "intent": "create_expense",
            "action": "create_draft",
            "reply_draft": "Struk kebaca sebagai draft pengeluaran.",
            "needs_confirmation": True,
            "needs_clarification": False,
            "clarification_prompt": "",
            "intent_candidates": [
                {
                    "intent": "create_expense",
                    "score": max(safe_float(ocr.get("receipt_confidence"), 0.7), total_confidence),
                    "reason": "receipt total detected",
                    "needs_reply": False,
                }
            ],
            "amount": amount,
            "currency": str(ocr.get("currency") or "IDR"),
            "merchant_name": str(ocr.get("merchant") or ""),
            "description": receipt_description(ocr, caption),
            "category_hint": infer_receipt_category(ocr),
            "account_hint": "",
            "transaction_date": normalize_receipt_date(ocr.get("date"), today),
            "transactions": [],
            "confidence": confidence,
            "missing_fields": [],
        }
    )


def normalize_chat_typos(text: str) -> str:
    replacements = {
        "abs": "abis",
        "abis": "abis",
        "hbis": "habis",
        "hbs": "habis",
        "brpa": "brapa",
        "brp": "brp",
        "bpa": "brapa",
        "hri": "hari",
        "hary": "hari",
        "tga": "tiga",
        "tig": "tiga",
        "dua": "dua",
        "kmrnny": "kmrnnya",
        "kmrny": "kmrnnya",
        "kmrnnya": "kmrnnya",
        "kmrn": "kmrn",
        "lgi": "lagi",
        "lg": "lagi",
        "mingu": "minggu",
        "mnggu": "minggu",
        "pngeluaran": "pengeluaran",
        "pngeluarannya": "pengeluaran",
        "smipan": "simpan",
        "simapn": "simpan",
        "simpn": "simpn",
        "spmn": "spmn",
        "gjdi": "gajadi",
        "gjd": "gajadi",
    }

    normalized_tokens: list[str] = []
    for token in re.findall(r"\w+|[^\w\s]", text, flags=re.UNICODE):
        lower = token.lower()
        replacement = replacements.get(lower)
        normalized_tokens.append(replacement if replacement is not None else token)

    value = ""
    for token in normalized_tokens:
        if re.match(r"[^\w\s]", token, flags=re.UNICODE):
            value += token
        else:
            if value and not value.endswith(" "):
                value += " "
            value += token
    value = re.sub(r"\bg\s+jadi\b", "gajadi", value, flags=re.IGNORECASE)
    value = re.sub(r"\bga\s+jdi\b", "gajadi", value, flags=re.IGNORECASE)
    value = re.sub(r"\bg\s+jdi\b", "gajadi", value, flags=re.IGNORECASE)
    return value.strip()


def local_route_decision(text: str, local: dict[str, Any]) -> dict[str, Any]:
    intent = str(local.get("intent") or "unknown")
    confidence = float(local.get("confidence") or 0)
    query = local.get("query")
    transactions = local.get("transactions")

    if is_typo_heavy_text(text):
        return {"use_local": False, "reason": "typo_heavy_needs_gemini"}
    if intent in {"confirm_draft", "cancel_flow"}:
        return {"use_local": True, "reason": "control_intent"}
    if intent in {"query_summary", "query_recent_transactions"} and isinstance(query, dict):
        if query.get("needs_clarification"):
            if is_typo_heavy_text(text):
                return {"use_local": False, "reason": "query_date_needs_gemini"}
            return {"use_local": True, "reason": "query_date_needs_clarification"}
        return {"use_local": True, "reason": "offline_query_fallback"}
    if intent == "create_multiple_transactions" and isinstance(transactions, list) and len(transactions) > 1:
        return {"use_local": True, "reason": "offline_multi_transaction_fallback"}
    if intent in {"create_expense", "create_income"} and confidence >= LOCAL_CONFIDENCE_THRESHOLD and is_simple_transaction_text(text):
        return {"use_local": True, "reason": "offline_simple_transaction_fallback"}
    return {"use_local": False, "reason": "needs_gemini"}


def is_typo_heavy_text(text: str) -> bool:
    lowered = text.lower()
    typo_markers = [
        "pluh",
        "pulh",
        "duwa",
        "hri",
        "llu",
        "brpa",
        "brsapa",
        "bpa",
        "pngeluaran",
        "mnggu",
        "mingu",
        "kmrnny",
    ]
    marker_count = sum(1 for marker in typo_markers if marker in lowered)
    if marker_count >= 2:
        return True

    tokens = re.findall(r"[a-zA-Z]+", lowered)
    if len(tokens) < 4:
        return False

    known = {
        "aku",
        "gw",
        "gua",
        "gue",
        "td",
        "tadi",
        "ke",
        "di",
        "dari",
        "buat",
        "apa",
        "aja",
        "abis",
        "habis",
        "berapa",
        "brapa",
        "brp",
        "hari",
        "minggu",
        "bulan",
        "lalu",
        "ini",
        "kemarin",
        "kmrn",
        "nya",
        "lagi",
        "makan",
        "beli",
        "pengeluaran",
        "pemasukan",
        "transaksi",
    }
    unknown_short = [token for token in tokens if len(token) <= 5 and token not in known and not token.isdigit()]
    return len(unknown_short) >= 3


def looks_like_messy_query(text: str) -> bool:
    lowered = text.lower()
    return any(word in lowered for word in ["hari lalu", "minggu lalu", "bulan lalu", "abis", "habis"]) and is_typo_heavy_text(lowered)


def messy_query_clarification(text: str) -> dict[str, Any]:
    today = datetime.now().date()
    return normalize_parse(
        {
            "intent": "query_summary",
            "_source_text": text,
            "intent_candidates": [
                {
                    "intent": "query_summary",
                    "score": 0.52,
                    "reason": "looks like a query but date wording is typo-heavy",
                    "needs_reply": True,
                }
            ],
            "currency": "IDR",
            "description": text,
            "category_hint": "",
            "account_hint": "",
            "transaction_date": today.isoformat(),
            "transactions": [],
            "query": {
                "metric": "expense_total",
                "type": "expense",
                "date_range": date_range("", "today_default", today, today, 0.35),
            },
            "confidence": 0.45,
            "missing_fields": [],
        }
    )


def unknown_month_date_clarification(text: str) -> dict[str, Any]:
    today = datetime.now().date()
    metric, tx_type = query_metric_and_type(text)
    date_range_value = date_range("", "unknown_month_date", today, today, 0.35)
    return {
        "intent": "query_recent_transactions" if metric == "transaction_list" else "query_summary",
        "action": "ask_clarification",
        "reply_draft": query_clarification_prompt(date_range_value),
        "needs_confirmation": False,
        "needs_clarification": True,
        "clarification_prompt": query_clarification_prompt(date_range_value),
        "intent_candidates": [
            {
                "intent": "query_recent_transactions" if metric == "transaction_list" else "query_summary",
                "score": 0.52,
                "reason": "looks like a date query but month wording is not locally recognized",
                "needs_reply": True,
            }
        ],
        "amount": None,
        "currency": "IDR",
        "description": text,
        "category_hint": "",
        "account_hint": "",
        "transaction_date": today.isoformat(),
        "transactions": [],
        "query": {
            "metric": metric,
            "type": tx_type,
            "date_range": date_range_value,
            "needs_clarification": True,
            "clarification_prompt": query_clarification_prompt(date_range_value),
        },
        "confidence": 0.45,
        "missing_fields": [],
    }


def detect_unknown_month_date_phrase(text: str) -> str | None:
    lowered = text.lower()
    if not any(word in lowered for word in ["abis", "habis", "pengeluaran", "pemasukan", "transaksi", "berapa", "brp", "brapa", "total", "totalnya"]):
        return None

    known_months = set(indonesian_months())
    candidates: list[tuple[str, str]] = []
    for match in re.finditer(r"\bbulan\s+(?P<token>[a-z]{3,12})\b", lowered):
        candidates.append((match.group("token"), match.group(0)))
    for match in re.finditer(r"\b(?P<token>[a-z]{3,12})\s+tahun\s+(?:kmrn|kemarin|lalu)\b", lowered):
        candidates.append((match.group("token"), match.group(0)))

    full_months = {name for name in known_months if len(name) > 3}
    non_month_date_words = {"ini", "lalu", "kmrn", "kemarin", "terakhir"}
    for token, phrase in candidates:
        if token in known_months or token in non_month_date_words:
            continue
        if min_distance(token, full_months) <= 2:
            return phrase
    return None


def is_simple_transaction_text(text: str) -> bool:
    lowered = text.lower()
    if len(text) > 80:
        return False
    if any(word in lowered for word in ["keknya", "kayaknya", "mungkin", "lupa", "sekitar", "kurang lebih"]):
        return False
    amount_count = len(re.findall(r"\b(?:rp\s*)?[0-9]+(?:[\.\,][0-9]{3})*(?:\s*(?:rb|ribu|k))?\b", lowered))
    if amount_count != 1:
        return False
    return True


def parse_short_command(text: str) -> dict[str, Any] | None:
    normalized = re.sub(r"\s+", " ", text.strip().lower())
    command = classify_short_command(normalized)
    if command == "confirm":
        return normalize_parse(
            {
                "intent": "confirm_draft",
                "intent_candidates": [
                    {
                        "intent": "confirm_draft",
                        "score": 0.96,
                        "reason": "short confirmation command",
                        "needs_reply": False,
                    }
                ],
                "currency": "IDR",
                "description": "",
                "category_hint": "",
                "account_hint": "",
                "transaction_date": datetime.now().date().isoformat(),
                "transactions": [],
                "confidence": 0.96,
                "missing_fields": [],
            }
        )
    if command == "cancel":
        return normalize_parse(
            {
                "intent": "cancel_flow",
                "intent_candidates": [
                    {
                        "intent": "cancel_flow",
                        "score": 0.96,
                        "reason": "short cancellation command",
                        "needs_reply": False,
                    }
                ],
                "currency": "IDR",
                "description": "",
                "category_hint": "",
                "account_hint": "",
                "transaction_date": datetime.now().date().isoformat(),
                "transactions": [],
                "confidence": 0.96,
                "missing_fields": [],
            }
        )
    return None


def classify_short_command(normalized: str) -> str | None:
    compact = re.sub(r"[^a-z0-9]", "", normalized)
    if not compact or len(normalized.split()) > 3:
        return None

    confirm_aliases = {
        "simpan",
        "smpan",
        "simpn",
        "simpen",
        "smpn",
        "spmn",
        "save",
        "confirm",
        "konfirmasi",
        "konfirm",
        "ya",
        "y",
        "iya",
        "iy",
        "iyah",
        "yes",
        "ok",
        "oke",
        "okay",
        "gas",
        "gass",
        "lanjut",
        "lanjot",
    }
    cancel_aliases = {
        "batal",
        "btl",
        "cancel",
        "cencel",
        "ga",
        "g",
        "gx",
        "gk",
        "ngga",
        "nggak",
        "ngga jadi",
        "nggak jadi",
        "ga jadi",
        "gajadi",
        "gjd",
        "gjadi",
        "tidak",
        "tdk",
        "tidak jadi",
        "no",
        "n",
        "nope",
    }
    compact_confirm = {re.sub(r"[^a-z0-9]", "", item) for item in confirm_aliases}
    compact_cancel = {re.sub(r"[^a-z0-9]", "", item) for item in cancel_aliases}

    if compact in compact_confirm:
        return "confirm"
    if compact in compact_cancel:
        return "cancel"
    return None


def min_distance(value: str, candidates: set[str]) -> int:
    return min(levenshtein(value, candidate) for candidate in candidates)


def levenshtein(left: str, right: str) -> int:
    if left == right:
        return 0
    if not left:
        return len(right)
    if not right:
        return len(left)

    previous = list(range(len(right) + 1))
    for i, left_char in enumerate(left, start=1):
        current = [i]
        for j, right_char in enumerate(right, start=1):
            insert = current[j - 1] + 1
            delete = previous[j] + 1
            replace = previous[j - 1] + (0 if left_char == right_char else 1)
            current.append(min(insert, delete, replace))
        previous = current
    return previous[-1]


def parse_with_offline_fallback(text: str) -> dict[str, Any]:
    """Best-effort parser for local development when Gemini is unavailable."""
    lowered = text.lower()
    transaction_date = datetime.now().date().isoformat()
    query = infer_query(lowered)
    amount_source_text = strip_query_date_mentions(text) if query is not None else text
    amount_source_lowered = amount_source_text.lower()
    transactions = extract_transaction_parts(amount_source_text, transaction_date, infer_account(lowered))
    amount = sum(item["amount"] for item in transactions) if transactions else extract_amount(amount_source_lowered)
    intent_candidates = infer_intent_candidates(lowered, amount, len(transactions), query is not None)
    intent = intent_candidates[0]["intent"]
    description = cleanup_description(text, amount)
    confidence = local_confidence(intent, amount, transactions, query)
    return normalize_parse(
        {
            "intent": intent,
            "_source_text": text,
            "intent_candidates": intent_candidates,
            "amount": amount,
            "currency": "IDR",
            "description": description,
            "category_hint": infer_category(lowered),
            "account_hint": infer_account(lowered),
            "transaction_date": transaction_date,
            "query": query,
            "transactions": transactions,
            "confidence": confidence,
            "missing_fields": [] if amount or intent.startswith("query_") else ["amount"],
        }
    )


def local_confidence(intent: str, amount: int | None, transactions: list[dict[str, Any]], query: dict[str, Any] | None) -> float:
    if intent in {"confirm_draft", "cancel_flow"}:
        return 0.96
    if intent in {"query_summary", "query_recent_transactions"} and query is not None:
        date_confidence = float(query.get("date_range", {}).get("confidence", 0.8))
        return min(0.92, max(0.70, date_confidence))
    if intent == "create_multiple_transactions" and len(transactions) > 1:
        return 0.84
    if intent in {"create_expense", "create_income"} and amount is not None:
        return 0.82
    return 0.45


def strip_query_date_mentions(text: str) -> str:
    month_names = "|".join(re.escape(name) for name in indonesian_months())
    value = re.sub(
        rf"\bdari\s+(?:tanggal\s+)?\d{{1,2}}(?:\s+(?:{month_names}))?(?:\s+\d{{4}})?\s+"
        rf"(?:sampai|sampe|hingga|s/d|-)\s+(?:tanggal\s+)?\d{{1,2}}(?:\s+(?:{month_names}))?(?:\s+\d{{4}})?\b",
        " ",
        text,
        flags=re.IGNORECASE,
    )
    value = re.sub(r"\b\d{1,2}\s+(?:hari|minggu|bulan)\s+terakhir\b", " ", value, flags=re.IGNORECASE)
    value = re.sub(r"\b\d{1,2}\s+(?:hari|minggu|bulan)\s+(?:yang\s+)?lalu\b", " ", value, flags=re.IGNORECASE)
    value = re.sub(r"\bbulan\s+(?:" + month_names + r")(?:\s+\d{4})?\b", " ", value, flags=re.IGNORECASE)
    return re.sub(r"\s+", " ", value).strip()


def extract_amount(text: str) -> int | None:
    patterns = [
        r"\brp\s*([0-9][0-9\.\,]*)\b",
        r"\b([0-9]+(?:[\.\,][0-9]{3})+)(?:,\d{2})?\b",
        r"\b([0-9]+)\s*(?:rb|ribu|k)\b",
        r"\b([0-9]{4,})\b",
    ]
    for pattern in patterns:
        match = re.search(pattern, text)
        if not match:
            continue
        raw = match.group(1)
        if re.search(r"(rb|ribu|k)\b", match.group(0)):
            return int(raw) * 1000
        normalized = re.sub(r"[^0-9]", "", raw)
        if normalized:
            return int(normalized)
    return None


def extract_transaction_parts(text: str, transaction_date: str, account_hint: str) -> list[dict[str, Any]]:
    parts: list[dict[str, Any]] = []
    pattern = re.compile(
        r"(?P<description>[a-zA-ZÀ-ÿ0-9\s&]+?)\s+(?P<amount>[0-9]+(?:[\.\,][0-9]{3})*|[0-9]+)\s*(?P<suffix>rb|ribu|k)?\b",
        re.IGNORECASE,
    )
    for match in pattern.finditer(text):
        raw_description = match.group("description")
        raw_description = re.sub(
            r"\b(td|tadi|terus|lalu|dan|abis|habis|kena|bayar)\b",
            " ",
            raw_description,
            flags=re.IGNORECASE,
        )
        description = re.sub(r"\s+", " ", raw_description).strip(" ,.-")
        raw_amount = match.group("amount")
        suffix = (match.group("suffix") or "").lower()
        amount = int(re.sub(r"[^0-9]", "", raw_amount))
        if suffix in {"rb", "ribu", "k"}:
            amount *= 1000
        if description and amount > 0:
            parts.append(
                {
                    "type": "expense",
                    "amount": amount,
                    "currency": "IDR",
                    "description": description,
                    "category_hint": infer_category(description.lower()),
                    "account_hint": account_hint,
                    "transaction_date": transaction_date,
                }
            )
    return parts if len(parts) > 1 else []


def infer_query(text: str) -> dict[str, Any] | None:
    date_range_value = normalize_date_range(text)
    if not looks_like_query_text(text):
        return None

    metric, tx_type = query_metric_and_type(text)
    return {
        "metric": metric,
        "type": tx_type,
        "date_range": date_range_value,
    }


def looks_like_query_text(text: str) -> bool:
    if any(phrase in text for phrase in ["apa aja", "beli apa", "transaksi apa", "jajan apa"]):
        return True
    query_tokens = {
        "brp",
        "brpa",
        "brapa",
        "berapa",
        "total",
        "totalnya",
        "pengeluaran",
        "pemasukan",
        "income",
        "spending",
        "habis",
        "abis",
    }
    tokens = set(re.findall(r"\b[a-z]+\b", text.lower()))
    return bool(tokens & query_tokens)


def query_metric_and_type(text: str) -> tuple[str, str]:
    tx_type = "income" if any(word in text for word in ["income", "pemasukan", "gaji", "masuk"]) else "expense"
    if any(word in text for word in ["beli apa", "apa aja", "transaksi apa", "jajan apa"]):
        return "transaction_list", tx_type
    if tx_type == "income":
        return "income_total", tx_type
    return "expense_total", tx_type


def normalize_parse(parsed: dict[str, Any]) -> dict[str, Any]:
    source_text = str(parsed.get("_source_text") or parsed.get("description") or "")
    amount = parse_int_amount(parsed.get("amount"))
    transactions = normalize_transactions(parsed)
    if amount is None and transactions:
        amount = sum(int(transaction["amount"]) for transaction in transactions)

    intent = normalize_intent_from_source(str(parsed.get("intent") or "unknown"), source_text, parsed.get("action"), amount)

    missing_fields = parsed.get("missing_fields")
    if not isinstance(missing_fields, list):
        missing_fields = []
    missing_fields = normalize_missing_fields(missing_fields, parsed)

    query = normalize_query(parsed)
    needs_clarification = bool(parsed.get("needs_clarification"))
    clarification_prompt = str(parsed.get("clarification_prompt") or "")
    if query is not None and query.get("needs_clarification"):
        needs_clarification = True
        clarification_prompt = clarification_prompt or str(query.get("clarification_prompt") or "")
    action = normalize_action(parsed.get("action"), intent, needs_clarification)
    needs_confirmation = bool(parsed.get("needs_confirmation"))
    if action in {"create_draft", "edit_draft"}:
        needs_confirmation = True
    if intent in {"create_expense", "create_income", "create_multiple_transactions"} and amount is not None and not missing_fields:
        needs_clarification = False
        clarification_prompt = ""
        action = "create_draft"
        needs_confirmation = True

    return {
        "intent": intent,
        "action": action,
        "reply_draft": str(parsed.get("reply_draft") or default_reply_draft(intent, action, needs_clarification)),
        "needs_confirmation": needs_confirmation,
        "needs_clarification": needs_clarification,
        "clarification_prompt": clarification_prompt,
        "intent_candidates": normalize_intent_candidates(parsed),
        "amount": amount,
        "currency": str(parsed.get("currency") or "IDR"),
        "merchant_name": str(parsed.get("merchant_name") or ""),
        "description": str(parsed.get("description") or ""),
        "category_hint": normalize_category(parsed.get("category_hint")),
        "account_hint": str(parsed.get("account_hint") or ""),
        "transaction_date": str(parsed.get("transaction_date") or datetime.now().date().isoformat()),
        "transactions": transactions,
        "query": query,
        "edit": normalize_edit(parsed),
        "confidence": float(parsed.get("confidence") or 0),
        "missing_fields": [str(item) for item in missing_fields],
    }


def normalize_intent_from_source(intent: str, source_text: str, action: Any = None, amount: int | None = None) -> str:
    allowed = {
        "create_expense",
        "create_income",
        "create_multiple_transactions",
        "query_summary",
        "query_recent_transactions",
        "edit_draft",
        "confirm_draft",
        "cancel_flow",
        "help",
        "unknown",
    }
    intent = intent.strip()
    if intent not in allowed:
        intent = "unknown"
    lowered = source_text.lower()
    if intent == "unknown" and re.search(r"\b(?:hapus|delete|remove)\b", lowered):
        return "edit_draft"
    if intent == "unknown" and str(action or "").strip() == "create_draft" and amount is not None and amount > 0:
        return "create_expense"
    return intent


def normalize_missing_fields(missing_fields: list[Any], parsed: dict[str, Any]) -> list[str]:
    normalized = [str(item) for item in missing_fields]
    if parsed.get("transaction_date"):
        normalized = [item for item in normalized if item != "transaction_date"]
    if parsed.get("amount") not in (None, ""):
        normalized = [item for item in normalized if item != "amount"]
    return normalized


def normalize_action(value: Any, intent: str, needs_clarification: bool) -> str:
    allowed = {"create_draft", "run_query", "edit_draft", "confirm_draft", "cancel_flow", "show_help", "ask_clarification", "none"}
    action = str(value or "").strip()
    if action == "none" and intent == "edit_draft":
        return "edit_draft"
    if action in allowed:
        return "ask_clarification" if needs_clarification else action
    if needs_clarification:
        return "ask_clarification"
    if intent in {"create_expense", "create_income", "create_multiple_transactions"}:
        return "create_draft"
    if intent in {"query_summary", "query_recent_transactions"}:
        return "run_query"
    if intent == "edit_draft":
        return "edit_draft"
    if intent == "confirm_draft":
        return "confirm_draft"
    if intent == "cancel_flow":
        return "cancel_flow"
    if intent == "help":
        return "show_help"
    return "none"


def default_reply_draft(intent: str, action: str, needs_clarification: bool) -> str:
    if needs_clarification or action == "ask_clarification":
        return "Aku belum yakin maksudnya. Bisa tulis lagi lebih jelas?"
    if action == "run_query":
        return "Aku cek datanya dulu ya."
    if action == "create_draft":
        return "Aku buat draft transaksinya dulu ya."
    if action == "edit_draft":
        return "Aku update draft-nya dulu ya."
    if action == "confirm_draft":
        return "Siap, aku proses konfirmasinya."
    if action == "cancel_flow":
        return "Siap, aku batalkan."
    if intent == "help":
        return "Kirim transaksi atau pertanyaan pengeluaran lewat chat."
    return ""


def normalize_transactions(parsed: dict[str, Any]) -> list[dict[str, Any]]:
    transactions = parsed.get("transactions")
    if not isinstance(transactions, list):
        return []

    normalized: list[dict[str, Any]] = []
    fallback_date = str(parsed.get("transaction_date") or datetime.now().date().isoformat())
    fallback_account = str(parsed.get("account_hint") or "")
    for transaction in transactions:
        if not isinstance(transaction, dict):
            continue
        raw_amount = transaction.get("amount")
        amount = parse_int_amount(raw_amount)
        if amount is None:
            continue
        normalized.append(
            {
                "type": str(transaction.get("type") or "expense"),
                "amount": amount,
                "currency": str(transaction.get("currency") or parsed.get("currency") or "IDR"),
                "merchant_name": str(transaction.get("merchant_name") or parsed.get("merchant_name") or ""),
                "description": str(transaction.get("description") or ""),
                "category_hint": normalize_category(transaction.get("category_hint")),
                "account_hint": str(transaction.get("account_hint") or fallback_account),
                "transaction_date": str(transaction.get("transaction_date") or fallback_date),
            }
        )
    return normalized


def normalize_edit(parsed: dict[str, Any]) -> dict[str, Any] | None:
    if str(parsed.get("intent") or "") != "edit_draft" and str(parsed.get("action") or "") != "edit_draft":
        return None
    edit = parsed.get("edit")
    if not isinstance(edit, dict):
        edit = {}
    normalized: dict[str, Any] = {
        "field": str(edit.get("field") or inferred_edit_field(parsed)),
        "value": edit.get("value") if edit.get("value") is not None else "",
        "category_hint": normalize_category(edit.get("category_hint")),
        "description": str(edit.get("description") or ""),
    }
    target = edit.get("target_item_index")
    try:
        if target not in (None, "") and int(target) <= 0:
            target = None
    except (TypeError, ValueError):
        target = None
    if target in (None, ""):
        target = inferred_edit_target(parsed)
    if target not in (None, ""):
        try:
            normalized["target_item_index"] = int(target)
        except (TypeError, ValueError):
            pass
    amount = edit.get("amount") if edit.get("amount") not in (None, "") else parsed.get("amount")
    parsed_amount = parse_int_amount(amount)
    if parsed_amount is not None:
        normalized["amount"] = parsed_amount
    if normalized["field"] == "category" and not normalized["category_hint"]:
        normalized["category_hint"] = normalize_category(parsed.get("category_hint"))
    return normalized


def inferred_edit_field(parsed: dict[str, Any]) -> str:
    if parsed.get("amount") not in (None, ""):
        return "amount"
    if normalize_category(parsed.get("category_hint")):
        return "category"
    description = str(parsed.get("description") or "").lower()
    if any(word in description for word in ["hapus", "delete", "remove"]):
        return "delete_item"
    return "unknown"


def inferred_edit_target(parsed: dict[str, Any]) -> int | None:
    source = str(parsed.get("_source_text") or parsed.get("description") or "").lower()
    ordinals = {
        "satu": 1,
        "pertama": 1,
        "dua": 2,
        "kedua": 2,
        "tiga": 3,
        "ketiga": 3,
        "empat": 4,
        "keempat": 4,
        "lima": 5,
        "kelima": 5,
    }
    for word, index in ordinals.items():
        if re.search(rf"\b(?:(?:yang|yg|nomor|no|item)\s+)?{word}\b", source):
            return index
    match = re.search(r"\b(?:item|nomor|no|yang|yg)?\s*(\d{1,2})\s+(?:harusnya|hrsnya|harunsya|jadi|diganti|ganti)\b", source)
    if match:
        return int(match.group(1))
    return None


def normalize_query(parsed: dict[str, Any]) -> dict[str, Any] | None:
    query = parsed.get("query")
    intent = str(parsed.get("intent") or "")
    if not intent.startswith("query_"):
        return None
    if not isinstance(query, dict):
        query = {}

    raw_candidates = [
        str(query.get("raw_text") or ""),
        str(parsed.get("_source_text") or ""),
        str(parsed.get("description") or ""),
        str((query.get("date_range") or {}).get("raw_text") if isinstance(query.get("date_range"), dict) else ""),
    ]
    raw_text = next((item for item in raw_candidates if item), "")
    date_range_value = normalize_date_range(raw_text) if raw_text else default_query_date_range()
    gemini_date_range = normalized_external_date_range(query.get("date_range"))
    if date_range_value["confidence"] < 0.60 and gemini_date_range is not None:
        date_range_value = gemini_date_range

    source_text = " ".join(raw_candidates).lower()
    date_range_value = validate_query_date_against_source(source_text, date_range_value)
    tx_type = str(query.get("type") or "")
    if not tx_type:
        tx_type = "income" if any(word in source_text for word in ["income", "pemasukan", "gaji", "masuk"]) else "expense"

    metric = str(query.get("metric") or "")
    if not metric:
        if intent == "query_recent_transactions":
            metric = "transaction_list"
        elif tx_type == "income":
            metric = "income_total"
        else:
            metric = "expense_total"
    needs_clarification = date_range_value["confidence"] < 0.60
    return {
        "metric": metric,
        "type": tx_type,
        "date_range": date_range_value,
        "needs_clarification": needs_clarification,
        "clarification_prompt": query_clarification_prompt(date_range_value) if needs_clarification else "",
    }


def validate_query_date_against_source(source_text: str, date_range_value: dict[str, Any]) -> dict[str, Any]:
    if not detect_unknown_month_date_phrase(source_text):
        invalid_range = looks_like_invalid_month_span(source_text, date_range_value) or looks_like_invalid_quarter(source_text, date_range_value)
        if not invalid_range:
            return date_range_value
        today = datetime.now().date()
        return date_range("", "ambiguous_date_range", today, today, 0.35)
    if not re.search(r"\btahun\s+(?:kmrn|kemarin|lalu)\b", source_text):
        return date_range_value
    try:
        start = datetime.strptime(str(date_range_value.get("start_date")), "%Y-%m-%d").date()
    except (TypeError, ValueError):
        start = None
    previous_year = datetime.now().date().year - 1
    if start is not None and start.year == previous_year:
        return date_range_value
    today = datetime.now().date()
    return date_range("", "unknown_month_date", today, today, 0.35)


def looks_like_invalid_month_span(source_text: str, date_range_value: dict[str, Any]) -> bool:
    months = indonesian_months()
    month_names = sorted(months, key=len, reverse=True)
    pattern = r"\b(" + "|".join(re.escape(name) for name in month_names) + r")\b\s*(?:sampai|sampe|hingga|ke|-)\s*\b(" + "|".join(re.escape(name) for name in month_names) + r")\b"
    match = re.search(pattern, source_text)
    if not match:
        return False
    start_month = months[match.group(1)]
    end_month = months[match.group(2)]
    if start_month == end_month:
        return False
    try:
        start = datetime.strptime(str(date_range_value.get("start_date")), "%Y-%m-%d").date()
        end = datetime.strptime(str(date_range_value.get("end_date")), "%Y-%m-%d").date()
    except (TypeError, ValueError):
        return True
    return start.month != start_month or end.month != end_month


def looks_like_invalid_quarter(source_text: str, date_range_value: dict[str, Any]) -> bool:
    match = re.search(r"\b(?:q|quarter\s*)([1-4])\b", source_text)
    if not match:
        return False
    quarter = int(match.group(1))
    expected_start_month = ((quarter - 1) * 3) + 1
    expected_end_month = expected_start_month + 2
    try:
        start = datetime.strptime(str(date_range_value.get("start_date")), "%Y-%m-%d").date()
        end = datetime.strptime(str(date_range_value.get("end_date")), "%Y-%m-%d").date()
    except (TypeError, ValueError):
        return True
    return start.month != expected_start_month or end.month != expected_end_month


def default_query_date_range() -> dict[str, Any]:
    today = datetime.now().date()
    return date_range("", "today_default", today, today, 0.35)


def normalized_external_date_range(value: Any) -> dict[str, Any] | None:
    if not isinstance(value, dict):
        return None
    start_raw = value.get("start_date")
    end_raw = value.get("end_date")
    if not start_raw or not end_raw:
        return None
    try:
        start = datetime.strptime(str(start_raw), "%Y-%m-%d").date()
        end = datetime.strptime(str(end_raw), "%Y-%m-%d").date()
    except ValueError:
        return None
    today = datetime.now().date()
    if start > end:
        return None
    if start > today:
        return None
    if (today - start).days > 3650:
        return None
    confidence = float(value.get("confidence") or 0.65)
    if confidence < 0.60:
        return None
    return {
        "raw_text": str(value.get("raw_text") or ""),
        "preset": str(value.get("preset") or "gemini_date_range"),
        "start_date": start.isoformat(),
        "end_date": end.isoformat(),
        "confidence": min(confidence, 0.82),
    }


def query_clarification_prompt(date_range_value: dict[str, Any]) -> str:
    preset = date_range_value.get("preset")
    if preset == "recent_past_ambiguous":
        return (
            "Maksudnya yang mana?\n"
            f"1. {date_range_value.get('end_date')}\n"
            f"2. {date_range_value.get('start_date')} s/d {date_range_value.get('end_date')}\n"
            "3. Tulis tanggal sendiri"
        )
    return "Aku belum yakin tanggalnya. Tulis tanggal atau rentang tanggalnya lagi."


def normalize_intent_candidates(parsed: dict[str, Any]) -> list[dict[str, Any]]:
    candidates = parsed.get("intent_candidates")
    if not isinstance(candidates, list):
        candidates = []

    normalized: list[dict[str, Any]] = []
    for candidate in candidates:
        if not isinstance(candidate, dict):
            continue
        normalized.append(
            {
                "intent": str(candidate.get("intent") or "unknown"),
                "score": float(candidate.get("score") or 0),
                "reason": str(candidate.get("reason") or ""),
                "needs_reply": bool(candidate.get("needs_reply") or False),
            }
        )

    if not normalized:
        normalized.append(
            {
                "intent": str(parsed.get("intent") or "unknown"),
                "score": float(parsed.get("confidence") or 0),
                "reason": "single parser result",
                "needs_reply": bool(parsed.get("missing_fields")),
            }
        )

    return sorted(normalized, key=lambda item: item["score"], reverse=True)[:4]


def main() -> None:
    server = ThreadingHTTPServer(("0.0.0.0", PORT), Handler)
    print(
        json.dumps(
            {
                "event": "inference_service_started",
                "addr": f":{PORT}",
                "gemini_enabled": bool(GEMINI_API_KEY),
                "model": GEMINI_MODEL,
            }
        ),
        flush=True,
    )
    server.serve_forever()


if __name__ == "__main__":
    main()
