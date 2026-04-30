#!/usr/bin/env python3
import json
import os
import re
import urllib.error
import urllib.request
from datetime import date, datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any


def load_env_file(path: str) -> None:
    if not os.path.exists(path):
        return
    with open(path, "r", encoding="utf-8") as file:
        for line in file:
            stripped = line.strip()
            if not stripped or stripped.startswith("#") or "=" not in stripped:
                continue
            key, value = stripped.split("=", 1)
            key = key.strip()
            value = value.strip().strip('"').strip("'")
            if key and key not in os.environ:
                os.environ[key] = value


load_env_file(".env")
load_env_file(".env.local")

PORT = int(os.getenv("INFERENCE_PORT", "8090"))
GEMINI_API_KEY = os.getenv("GEMINI_API_KEY", "")
GEMINI_MODEL = os.getenv("GEMINI_MODEL", "gemini-2.5-flash-lite")
GEMINI_TIMEOUT_SECONDS = int(os.getenv("GEMINI_TIMEOUT_SECONDS", "20"))
PARSER_VERSION = "2026-04-27.multi-transaction-v1"
LOCAL_CONFIDENCE_THRESHOLD = float(os.getenv("LOCAL_CONFIDENCE_THRESHOLD", "0.90"))
CATEGORY_HINTS = {
    "Makan & Minum",
    "Transport",
    "Belanja Harian",
    "Tagihan",
    "Hiburan",
    "Kesehatan",
    "Pendidikan",
    "Income",
    "Transfer",
    "Lainnya",
    "",
}
CATEGORY_ALIASES = {
    "makanan": "Makan & Minum",
    "makan": "Makan & Minum",
    "minuman": "Makan & Minum",
    "food": "Makan & Minum",
    "kuliner": "Makan & Minum",
    "restoran": "Makan & Minum",
    "restaurant": "Makan & Minum",
    "transportasi": "Transport",
    "transportation": "Transport",
    "belanja": "Belanja Harian",
    "groceries": "Belanja Harian",
    "grocery": "Belanja Harian",
    "hiburan": "Hiburan",
    "entertainment": "Hiburan",
    "kesehatan": "Kesehatan",
    "health": "Kesehatan",
    "pendidikan": "Pendidikan",
    "education": "Pendidikan",
    "pemasukan": "Income",
    "pendapatan": "Income",
    "income": "Income",
    "transfer": "Transfer",
    "lainnya": "Lainnya",
    "other": "Lainnya",
}


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
        if self.path != "/v1/parse/text":
            self.write_json(404, {"error": "not found"})
            return

        try:
            request = self.read_json()
            text = str(request.get("text") or "").strip()
            if not text:
                self.write_json(400, {"error": "text is required"})
                return

            parsed = route_parse(text)

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


def parse_with_gemini(text: str) -> dict[str, Any]:
    prompt = build_prompt(text)
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
            "transactions": {
                "type": "array",
                "items": {
                    "type": "object",
                    "properties": {
                        "type": {"type": "string"},
                        "amount": {"type": "integer"},
                        "currency": {"type": "string"},
                        "description": {"type": "string"},
                        "category_hint": {"type": "string"},
                        "account_hint": {"type": "string"},
                        "transaction_date": {"type": "string"},
                    },
                    "required": [
                        "type",
                        "amount",
                        "currency",
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
    url = (
        "https://generativelanguage.googleapis.com/v1beta/models/"
        + GEMINI_MODEL
        + ":generateContent?key="
        + GEMINI_API_KEY
    )
    req = urllib.request.Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )

    try:
        with urllib.request.urlopen(req, timeout=GEMINI_TIMEOUT_SECONDS) as resp:
            data = json.loads(resp.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"gemini HTTP {exc.code}: {detail}") from exc

    text_response = extract_gemini_text(data)
    parsed = json.loads(strip_json_fence(text_response))
    parsed["_source_text"] = text
    return normalize_parse(parsed)


def build_prompt(text: str) -> str:
    today = datetime.now().date().isoformat()
    return f"""
You are the conversational interpreter for an Indonesian WhatsApp finance bot.
Interpret the user's message and return one safe structured action.
Return only JSON matching the schema.

Allowed intents:
- create_expense
- create_income
- create_multiple_transactions
- query_summary
- query_recent_transactions
- edit_draft
- confirm_draft
- cancel_flow
- help
- unknown

Rules:
- action must be one of: create_draft, run_query, edit_draft, confirm_draft, cancel_flow, show_help, ask_clarification, none.
- Use create_draft for new expense/income/transfer drafts. Do not save anything.
- Use run_query for spending/income/history questions. Do not invent query results.
- Use edit_draft for corrections to an existing pending draft.
- Use confirm_draft only when the user clearly confirms a pending draft.
- Use cancel_flow only when the user clearly cancels.
- needs_confirmation is true for create_draft and edit_draft actions.
- needs_clarification is true when important details or dates are ambiguous.
- reply_draft should be a short Indonesian WhatsApp-style acknowledgement or clarification, not a final DB result.
- Currency is IDR unless clearly different.
- Amount must be integer rupiah, so "18rb" is 18000 and "47.500" is 47500.
- If there is no amount, omit the amount field.
- transaction_date is YYYY-MM-DD. Use {today} for today-relative messages.
- category_hint must be exactly one of:
  Makan & Minum, Transport, Belanja Harian, Tagihan, Hiburan,
  Kesehatan, Pendidikan, Income, Transfer, Lainnya, or empty string.
- account_hint should be e-wallet, bank, cash hint, or empty string.
- intent_candidates must contain the top 2-4 plausible intents, sorted by score descending.
- Each candidate reason should be short and useful for parser debugging.
- needs_reply is true when the bot should ask a clarification before saving.
- If the message contains multiple separate expenses or incomes, set intent to create_multiple_transactions.
- For multiple transactions, amount is the sum of all transaction amounts.
- For multiple transactions, transactions must contain one object per transaction.
- Do not merge separate activities into one transaction unless the user explicitly says it is one total.
- For query_summary or query_recent_transactions, include query.
- Query metric is expense_total for "habis brp/pengeluaran berapa" and transaction_list for "beli apa aja/transaksi apa aja".
- Query type is expense unless the user clearly asks income or all transactions.
- Date ranges must be exact YYYY-MM-DD dates based on today {today} and timezone Asia/Jakarta.
- If date wording is ambiguous, still include the best date_range and reduce date_range.confidence.
- Go owns state, persistence, query execution, and final replies. You only interpret the message.
- Do not claim anything was saved or queried successfully.

Message:
{text}
""".strip()


def extract_gemini_text(data: dict[str, Any]) -> str:
    candidates = data.get("candidates") or []
    if not candidates:
        raise RuntimeError("gemini returned no candidates")
    content = candidates[0].get("content") or {}
    parts = content.get("parts") or []
    texts = [str(part.get("text")) for part in parts if part.get("text") is not None]
    if not texts:
        raise RuntimeError("gemini returned no text")
    return "\n".join(texts)


def strip_json_fence(value: str) -> str:
    stripped = value.strip()
    if stripped.startswith("```"):
        stripped = re.sub(r"^```(?:json)?", "", stripped).strip()
        stripped = re.sub(r"```$", "", stripped).strip()
    return stripped


def route_parse(text: str) -> dict[str, Any]:
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
            gemini = parse_with_gemini(normalized_text)
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
    if not any(word in lowered for word in ["abis", "habis", "pengeluaran", "pemasukan", "transaksi", "berapa", "brp", "brapa"]):
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


def extract_date_range_with_gemini(text: str) -> dict[str, Any] | None:
    today = datetime.now().date().isoformat()
    prompt = f"""
Extract only the intended date range from this Indonesian finance query.
Return JSON only.

Today: {today}
Text: {text}

Rules:
- Interpret typo-heavy month names if possible.
- "tahun kemarin", "tahun kmrn", and "tahun lalu" mean previous calendar year.
- Return null if unsure.
- start_date and end_date must use YYYY-MM-DD.
- confidence must be 0 to 1.

JSON shape:
{{"raw_text":"...","preset":"gemini_date_range","start_date":"2025-02-01","end_date":"2025-02-28","confidence":0.8}}
""".strip()
    payload = {
        "contents": [{"parts": [{"text": prompt}]}],
        "generationConfig": {
            "temperature": 0,
            "responseMimeType": "application/json",
        },
    }
    url = (
        "https://generativelanguage.googleapis.com/v1beta/models/"
        + GEMINI_MODEL
        + ":generateContent?key="
        + GEMINI_API_KEY
    )
    req = urllib.request.Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=GEMINI_TIMEOUT_SECONDS) as resp:
            data = json.loads(resp.read().decode("utf-8"))
        parsed = json.loads(strip_json_fence(extract_gemini_text(data)))
    except Exception:
        return None
    return normalized_external_date_range(parsed)


def query_from_extracted_date_range(original_text: str, normalized_text: str, date_range_value: dict[str, Any], reason: str) -> dict[str, Any]:
    metric, tx_type = query_metric_and_type(normalized_text)
    needs_clarification = date_range_value["confidence"] < 0.60
    parsed = {
        "intent": "query_recent_transactions" if metric == "transaction_list" else "query_summary",
        "action": "ask_clarification" if needs_clarification else "run_query",
        "reply_draft": query_clarification_prompt(date_range_value) if needs_clarification else "Aku cek datanya dulu ya.",
        "needs_confirmation": False,
        "needs_clarification": needs_clarification,
        "clarification_prompt": query_clarification_prompt(date_range_value) if needs_clarification else "",
        "intent_candidates": [
            {
                "intent": "query_recent_transactions" if metric == "transaction_list" else "query_summary",
                "score": min(0.88, float(date_range_value["confidence"])),
                "reason": "Gemini extracted date range for unknown month wording",
                "needs_reply": needs_clarification,
            }
        ],
        "amount": None,
        "currency": "IDR",
        "description": normalized_text,
        "category_hint": "",
        "account_hint": "",
        "transaction_date": datetime.now().date().isoformat(),
        "transactions": [],
        "query": {
            "metric": metric,
            "type": tx_type,
            "date_range": date_range_value,
            "needs_clarification": needs_clarification,
            "clarification_prompt": query_clarification_prompt(date_range_value) if needs_clarification else "",
        },
        "confidence": min(0.88, float(date_range_value["confidence"])),
        "missing_fields": [],
        "raw": {
            "provider": "gemini_date_extraction",
            "model": GEMINI_MODEL,
            "gemini_called": True,
            "fallback_reason": reason,
            "original_text": original_text,
            "normalized_text": normalized_text,
            "parser_version": PARSER_VERSION,
        },
    }
    return parsed


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


def normalize_date_range(text: str) -> dict[str, Any]:
    today = datetime.now().date()
    lowered = text.lower()

    explicit_range = extract_explicit_date_range(lowered, today)
    if explicit_range is not None:
        return explicit_range
    rolling_period = extract_rolling_period(lowered, today)
    if rolling_period is not None:
        return rolling_period
    named_month = extract_named_month(lowered, today)
    if named_month is not None:
        return named_month

    if re.search(r"\bkemarin\s*-\s*kemarin\b", lowered) or any(phrase in lowered for phrase in ["kmrn kmrn", "beberapa hari lalu"]):
        start = today - timedelta(days=7)
        end = today - timedelta(days=1)
        return date_range("kemarin-kemarin", "recent_past_ambiguous", start, end, 0.45)
    if any(phrase in lowered for phrase in ["kmrnnya lagi", "kemarinnya lagi", "kemarin nya lagi"]):
        target = today - timedelta(days=2)
        return date_range("kmrnnya lagi", "day_before_yesterday", target, target, 0.86)
    days_ago = extract_days_ago(lowered)
    if days_ago is not None:
        target = today - timedelta(days=days_ago)
        return date_range(f"{days_ago} hari lalu", "days_ago", target, target, 0.9)
    if any(phrase in lowered for phrase in ["hari ini", "today", "td ", "tadi"]):
        return date_range("hari ini", "today", today, today, 0.93)
    months_ago = extract_period_ago(lowered, "bulan")
    if months_ago is not None:
        target = add_months(today.replace(day=1), -months_ago)
        end = last_day_of_month(target)
        return date_range(f"{months_ago} bulan lalu", "months_ago", target, end, 0.88)
    weeks_ago = extract_period_ago(lowered, "minggu")
    if weeks_ago is not None:
        start = start_of_week(today) - timedelta(days=7 * weeks_ago)
        end = start + timedelta(days=6)
        return date_range(f"{weeks_ago} minggu lalu", "weeks_ago", start, end, 0.88)
    if "minggu lalu" in lowered:
        start = start_of_week(today) - timedelta(days=7)
        end = start + timedelta(days=6)
        return date_range("minggu lalu", "last_week", start, end, 0.93)
    if "minggu ini" in lowered:
        start = start_of_week(today)
        return date_range("minggu ini", "this_week", start, today, 0.93)
    if "bulan lalu" in lowered:
        first_this_month = today.replace(day=1)
        end = first_this_month - timedelta(days=1)
        start = end.replace(day=1)
        return date_range("bulan lalu", "last_month", start, end, 0.93)
    if "bulan ini" in lowered:
        start = today.replace(day=1)
        return date_range("bulan ini", "this_month", start, today, 0.93)
    if "tahun lalu" in lowered:
        start = date(today.year - 1, 1, 1)
        end = date(today.year - 1, 12, 31)
        return date_range("tahun lalu", "last_year", start, end, 0.93)
    if "tahun ini" in lowered:
        start = date(today.year, 1, 1)
        return date_range("tahun ini", "this_year", start, today, 0.93)

    weekdays = {
        "senin": 0,
        "selasa": 1,
        "rabu": 2,
        "kamis": 3,
        "jumat": 4,
        "jum'at": 4,
        "sabtu": 5,
        "minggu": 6,
    }
    for label, weekday in weekdays.items():
        if re.search(rf"\b{re.escape(label)}\b", lowered):
            target = previous_weekday(today, weekday)
            confidence = 0.9 if any(word in lowered for word in ["kmrn", "kemarin", "lalu"]) else 0.78
            return date_range(label + (" kemarin" if any(word in lowered for word in ["kmrn", "kemarin"]) else ""), "previous_weekday", target, target, confidence)

    if re.search(r"\b(kmrn|kemarin)\b", lowered):
        target = today - timedelta(days=1)
        return date_range("kemarin", "yesterday", target, target, 0.95)
    return date_range("", "today_default", today, today, 0.35)


def previous_weekday(today: date, weekday: int) -> date:
    delta = (today.weekday() - weekday) % 7
    if delta == 0:
        delta = 7
    return today - timedelta(days=delta)


def extract_period_ago(text: str, unit: str) -> int | None:
    match = re.search(rf"\b(\d{{1,2}})\s*{unit}\s+(?:yang\s+)?lalu\b", text)
    if match:
        value = int(match.group(1))
        return value if 1 <= value <= 24 else None

    match = re.search(rf"\b([a-z]+(?:\s+[a-z]+){{0,3}})\s+{unit}\s+(?:yang\s+)?lalu\b", text)
    if not match:
        return None
    return word_number_to_int(match.group(1))


def extract_rolling_period(text: str, today: date) -> dict[str, Any] | None:
    match = re.search(r"\b(\d{1,2}|[a-z]+(?:\s+[a-z]+){0,3})\s+(hari|minggu|bulan)\s+terakhir\b", text)
    if not match:
        return None
    raw_value = match.group(1)
    unit = match.group(2)
    value = int(raw_value) if raw_value.isdigit() else word_number_to_int(raw_value)
    if value is None:
        return None
    if unit == "hari" and not 1 <= value <= 366:
        return None
    if unit == "minggu" and not 1 <= value <= 104:
        return None
    if unit == "bulan" and not 1 <= value <= 60:
        return None

    if unit == "hari":
        start = today - timedelta(days=value)
        preset = "last_n_days"
    elif unit == "minggu":
        start = today - timedelta(days=value * 7)
        preset = "last_n_weeks"
    else:
        start = add_months(today, -value)
        preset = "last_n_months"
    return date_range(f"{value} {unit} terakhir", preset, start, today, 0.9)


def extract_named_month(text: str, today: date) -> dict[str, Any] | None:
    months = indonesian_months()
    match = re.search(
        r"\b(?:bulan\s+)?("
        + "|".join(re.escape(name) for name in sorted(months, key=len, reverse=True))
        + r")(?:\s+(?:(\d{4})|tahun\s+(?:kemarin|kmrn|lalu)))?\b",
        text,
    )
    if not match:
        return None
    month_name = match.group(1)
    month = months[month_name]
    suffix = text[match.end() : match.end() + 30]
    suffix_year = re.match(r"\s+(\d{4})\b", suffix)
    explicit_year = match.group(2) or (suffix_year.group(1) if suffix_year else None)
    has_previous_year = bool(re.search(r"\btahun\s+(?:kemarin|kmrn|lalu)\b", match.group(0))) or bool(
        re.match(r"\s+tahun\s+(?:kemarin|kmrn|lalu)\b", suffix)
    )
    year = int(explicit_year) if explicit_year else today.year
    explicit_bulan_prefix = match.group(0).startswith("bulan ")
    if has_previous_year:
        year = today.year - 1
    elif explicit_bulan_prefix and not explicit_year and month > today.month:
        year -= 1
    start = date(year, month, 1)
    end = today if year == today.year and month == today.month else last_day_of_month(start)
    return date_range(f"bulan {month_name}", "named_month", start, end, 0.88)


def extract_explicit_date_range(text: str, today: date) -> dict[str, Any] | None:
    month_names = "|".join(re.escape(name) for name in indonesian_months())
    pattern = re.compile(
        rf"\bdari\s+(?:tanggal\s+)?(?P<start_day>\d{{1,2}})(?:\s+(?P<start_month>{month_names}))?(?:\s+(?P<start_year>\d{{4}}))?\s+"
        rf"(?:sampai|sampe|hingga|s/d|-)\s+(?:tanggal\s+)?(?P<end_day>\d{{1,2}})(?:\s+(?P<end_month>{month_names}))?(?:\s+(?P<end_year>\d{{4}}))?\b"
    )
    match = pattern.search(text)
    if not match:
        return None

    start_month = month_number(match.group("start_month")) or today.month
    end_month = month_number(match.group("end_month")) or start_month
    start_year = int(match.group("start_year")) if match.group("start_year") else today.year
    end_year = int(match.group("end_year")) if match.group("end_year") else start_year
    start_day = int(match.group("start_day"))
    end_day = int(match.group("end_day"))
    try:
        start = date(start_year, start_month, start_day)
        end = date(end_year, end_month, end_day)
    except ValueError:
        return None
    if start > end or start > today:
        return None
    end = min(end, today)
    return date_range(match.group(0), "explicit_date_range", start, end, 0.88)


def word_number_to_int(value: str) -> int | None:
    normalized = re.sub(r"\s+", " ", value.strip().lower())
    number_words = {
        "se": 1,
        "satu": 1,
        "dua": 2,
        "tiga": 3,
        "empat": 4,
        "lima": 5,
        "enam": 6,
        "tujuh": 7,
        "delapan": 8,
        "sembilan": 9,
        "sepuluh": 10,
        "sebelas": 11,
    }
    if normalized in number_words:
        return number_words[normalized]
    if normalized.startswith("dua puluh"):
        rest = normalized.removeprefix("dua puluh").strip()
        return 20 + (number_words.get(rest, 0) if rest else 0)
    if normalized.startswith("tiga puluh"):
        rest = normalized.removeprefix("tiga puluh").strip()
        return 30 + (number_words.get(rest, 0) if rest else 0)
    return None


def indonesian_months() -> dict[str, int]:
    return {
        "januari": 1,
        "jan": 1,
        "februari": 2,
        "feb": 2,
        "maret": 3,
        "mar": 3,
        "april": 4,
        "apr": 4,
        "mei": 5,
        "juni": 6,
        "jun": 6,
        "juli": 7,
        "jul": 7,
        "agustus": 8,
        "agu": 8,
        "agt": 8,
        "september": 9,
        "sep": 9,
        "oktober": 10,
        "okt": 10,
        "november": 11,
        "nov": 11,
        "desember": 12,
        "des": 12,
    }


def month_number(value: str | None) -> int | None:
    if not value:
        return None
    return indonesian_months().get(value.lower())


def add_months(value: date, months: int) -> date:
    month_index = value.month - 1 + months
    year = value.year + month_index // 12
    month = month_index % 12 + 1
    day = min(value.day, days_in_month(year, month))
    return date(year, month, day)


def last_day_of_month(value: date) -> date:
    if value.month == 12:
        return date(value.year, 12, 31)
    return date(value.year, value.month + 1, 1) - timedelta(days=1)


def days_in_month(year: int, month: int) -> int:
    if month == 12:
        return 31
    return (date(year, month + 1, 1) - timedelta(days=1)).day


def extract_days_ago(text: str) -> int | None:
    match = re.search(r"\b(\d{1,2})\s*hari\s+lalu\b", text)
    if match:
        value = int(match.group(1))
        return value if 1 <= value <= 31 else None

    match = re.search(r"\b([a-z]+(?:\s+[a-z]+){0,3})\s+hari\s+lalu\b", text)
    if not match:
        return None
    return word_number_to_int(match.group(1))


def start_of_week(value: date) -> date:
    return value - timedelta(days=value.weekday())


def date_range(raw_text: str, preset: str, start: date, end: date, confidence: float) -> dict[str, Any]:
    return {
        "raw_text": raw_text,
        "preset": preset,
        "start_date": start.isoformat(),
        "end_date": end.isoformat(),
        "confidence": confidence,
    }


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


def normalize_parse(parsed: dict[str, Any]) -> dict[str, Any]:
    intent = str(parsed.get("intent") or "unknown")
    amount = parsed.get("amount")
    if amount in ("", None):
        amount = None
    else:
        amount = int(amount)

    missing_fields = parsed.get("missing_fields")
    if not isinstance(missing_fields, list):
        missing_fields = []

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
        "description": str(parsed.get("description") or ""),
        "category_hint": normalize_category(parsed.get("category_hint")),
        "account_hint": str(parsed.get("account_hint") or ""),
        "transaction_date": str(parsed.get("transaction_date") or datetime.now().date().isoformat()),
        "transactions": normalize_transactions(parsed),
        "query": query,
        "confidence": float(parsed.get("confidence") or 0),
        "missing_fields": [str(item) for item in missing_fields],
    }


def normalize_action(value: Any, intent: str, needs_clarification: bool) -> str:
    allowed = {"create_draft", "run_query", "edit_draft", "confirm_draft", "cancel_flow", "show_help", "ask_clarification", "none"}
    action = str(value or "")
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


def normalize_category(value: Any) -> str:
    category = str(value or "").strip()
    if category in CATEGORY_HINTS:
        return category
    return CATEGORY_ALIASES.get(category.lower(), "Lainnya" if category else "")


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
        if raw_amount in ("", None):
            continue
        normalized.append(
            {
                "type": str(transaction.get("type") or "expense"),
                "amount": int(raw_amount),
                "currency": str(transaction.get("currency") or parsed.get("currency") or "IDR"),
                "description": str(transaction.get("description") or ""),
                "category_hint": normalize_category(transaction.get("category_hint")),
                "account_hint": str(transaction.get("account_hint") or fallback_account),
                "transaction_date": str(transaction.get("transaction_date") or fallback_date),
            }
        )
    return normalized


def normalize_query(parsed: dict[str, Any]) -> dict[str, Any] | None:
    query = parsed.get("query")
    intent = str(parsed.get("intent") or "")
    if not isinstance(query, dict):
        if not intent.startswith("query_"):
            return None
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
