#!/usr/bin/env python3
import importlib.util
import json
import os
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[2]
APP_PATH = ROOT / "services" / "inference" / "app.py"
FIXTURE_PATH = ROOT / "tests" / "fixtures" / "parser_cases.json"
FIXED_TODAY = datetime(2026, 4, 27)


class FixedDateTime(datetime):
    @classmethod
    def now(cls, tz=None):
        if tz is not None:
            return FIXED_TODAY.replace(tzinfo=tz)
        return FIXED_TODAY


def load_parser_module() -> Any:
    os.environ["GEMINI_API_KEY"] = ""
    spec = importlib.util.spec_from_file_location("inference_app", APP_PATH)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"failed to load {APP_PATH}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.GEMINI_API_KEY = ""
    module.datetime = FixedDateTime
    return module


def main() -> int:
    parser = load_parser_module()
    cases = json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))
    failures: list[str] = []

    for case in cases:
        parsed = parser.route_parse(case["input"])
        errors = check_case(case, parsed)
        if errors:
            failures.append(format_failure(case, parsed, errors))

    for case in validation_cases():
        parsed = parser.normalize_parse(case["parsed"])
        errors = check_case(case, parsed)
        if errors:
            failures.append(format_failure(case, parsed, errors))

    receipt_test_cases = receipt_cases()
    for case in receipt_test_cases:
        candidates = parser.extract_receipt_candidates(case["ocr"])
        parsed = parser.normalize_receipt_ocr(parser.ocr_candidates_to_receipt_ocr(candidates), "")
        errors = check_case(case, parsed)
        if errors:
            failures.append(format_failure(case, parsed, errors))

    if failures:
        print("\n\n".join(failures))
        print(f"\nFAILED: {len(failures)} / {len(cases)} parser cases failed")
        return 1

    print(f"OK: {len(cases)} parser cases and {len(receipt_test_cases)} receipt cases passed")
    return 0


def check_case(case: dict[str, Any], parsed: dict[str, Any]) -> list[str]:
    expect = case["expect"]
    errors: list[str] = []

    def assert_equal(label: str, actual: Any, expected: Any) -> None:
        if actual != expected:
            errors.append(f"{label}: expected {expected!r}, got {actual!r}")

    if "intent" in expect:
        assert_equal("intent", parsed.get("intent"), expect["intent"])
    if "action" in expect:
        assert_equal("action", parsed.get("action"), expect["action"])
    if "needs_confirmation" in expect:
        assert_equal("needs_confirmation", parsed.get("needs_confirmation"), expect["needs_confirmation"])
    if "top_needs_clarification" in expect:
        assert_equal("needs_clarification", parsed.get("needs_clarification"), expect["top_needs_clarification"])
    if "amount" in expect:
        assert_equal("amount", parsed.get("amount"), expect["amount"])
    if "category_hint" in expect:
        assert_equal("category_hint", parsed.get("category_hint"), expect["category_hint"])
    if "transaction_count" in expect:
        assert_equal("transaction_count", len(parsed.get("transactions") or []), expect["transaction_count"])
    edit = parsed.get("edit") or {}
    if "edit_field" in expect:
        assert_equal("edit.field", edit.get("field"), expect["edit_field"])
    if "edit_target_item_index" in expect:
        assert_equal("edit.target_item_index", edit.get("target_item_index"), expect["edit_target_item_index"])
    if "edit_amount" in expect:
        assert_equal("edit.amount", edit.get("amount"), expect["edit_amount"])

    raw = parsed.get("raw") or {}
    if "gemini_called" in expect:
        assert_equal("raw.gemini_called", raw.get("gemini_called"), expect["gemini_called"])
    if "normalized_text" in expect:
        assert_equal("raw.normalized_text", raw.get("normalized_text"), expect["normalized_text"])

    query = parsed.get("query") or {}
    date_range = query.get("date_range") or {}
    if "metric" in expect:
        assert_equal("query.metric", query.get("metric"), expect["metric"])
    if "query_type" in expect:
        assert_equal("query.type", query.get("type"), expect["query_type"])
    if "needs_clarification" in expect:
        assert_equal("query.needs_clarification", query.get("needs_clarification"), expect["needs_clarification"])
    if "date_preset" in expect:
        assert_equal("query.date_range.preset", date_range.get("preset"), expect["date_preset"])
    if "date_confidence_max" in expect:
        confidence = float(date_range.get("confidence") or 0)
        if confidence > expect["date_confidence_max"]:
            errors.append(f"query.date_range.confidence: expected <= {expect['date_confidence_max']}, got {confidence}")
    if "date_offset_start_days" in expect:
        expected = (today() + timedelta(days=expect["date_offset_start_days"])).isoformat()
        assert_equal("query.date_range.start_date", date_range.get("start_date"), expected)
    if "date_offset_end_days" in expect:
        expected = (today() + timedelta(days=expect["date_offset_end_days"])).isoformat()
        assert_equal("query.date_range.end_date", date_range.get("end_date"), expected)
    if "date_weekday" in expect:
        start_date = parse_date(date_range.get("start_date"))
        if start_date.weekday() != expect["date_weekday"]:
            errors.append(f"query.date_range.start_date weekday: expected {expect['date_weekday']}, got {start_date.weekday()}")
    if "date_start_weekday" in expect:
        start_date = parse_date(date_range.get("start_date"))
        if start_date.weekday() != expect["date_start_weekday"]:
            errors.append(f"query.date_range.start_date weekday: expected {expect['date_start_weekday']}, got {start_date.weekday()}")
    if "date_start_day" in expect:
        start_date = parse_date(date_range.get("start_date"))
        if start_date.day != expect["date_start_day"]:
            errors.append(f"query.date_range.start_date day: expected {expect['date_start_day']}, got {start_date.day}")
    if "date_month_offset" in expect:
        start_date = parse_date(date_range.get("start_date"))
        expected_month_start = add_months(today().replace(day=1), expect["date_month_offset"])
        if start_date.year != expected_month_start.year or start_date.month != expected_month_start.month:
            errors.append(
                "query.date_range.start_date month: "
                f"expected {expected_month_start.year}-{expected_month_start.month:02d}, "
                f"got {start_date.year}-{start_date.month:02d}"
            )
    if "date_offset_start_months" in expect:
        expected = add_months(today(), expect["date_offset_start_months"]).isoformat()
        assert_equal("query.date_range.start_date", date_range.get("start_date"), expected)
    if "date_end_today" in expect:
        assert_equal("query.date_range.end_date", date_range.get("end_date"), today().isoformat())
    if "date_start_month" in expect:
        start_date = parse_date(date_range.get("start_date"))
        if start_date.month != expect["date_start_month"]:
            errors.append(f"query.date_range.start_date month: expected {expect['date_start_month']}, got {start_date.month}")
    if "date_start_year_offset" in expect:
        start_date = parse_date(date_range.get("start_date"))
        expected_year = today().year + expect["date_start_year_offset"]
        if start_date.year != expected_year:
            errors.append(f"query.date_range.start_date year: expected {expected_year}, got {start_date.year}")
    if "date_end_year_offset" in expect:
        end_date = parse_date(date_range.get("end_date"))
        expected_year = today().year + expect["date_end_year_offset"]
        if end_date.year != expected_year:
            errors.append(f"query.date_range.end_date year: expected {expected_year}, got {end_date.year}")
    if "date_end_month" in expect:
        end_date = parse_date(date_range.get("end_date"))
        if end_date.month != expect["date_end_month"]:
            errors.append(f"query.date_range.end_date month: expected {expect['date_end_month']}, got {end_date.month}")
    if "date_end_day" in expect:
        end_date = parse_date(date_range.get("end_date"))
        if end_date.day != expect["date_end_day"]:
            errors.append(f"query.date_range.end_date day: expected {expect['date_end_day']}, got {end_date.day}")
    if "date_start_iso" in expect:
        assert_equal("query.date_range.start_date", date_range.get("start_date"), expect["date_start_iso"])
    if "date_end_iso" in expect:
        assert_equal("query.date_range.end_date", date_range.get("end_date"), expect["date_end_iso"])

    return errors


def validation_cases() -> list[dict[str, Any]]:
    return [
        {
            "name": "validator rejects collapsed month span",
            "input": "juni sampai juli totalnya?",
            "parsed": gemini_query_stub("juni sampai juli totalnya?", "2026-06-01", "2026-06-30"),
            "expect": {
                "intent": "query_summary",
                "action": "ask_clarification",
                "top_needs_clarification": True,
                "date_preset": "ambiguous_date_range",
            },
        },
        {
            "name": "validator rejects broad q1 range",
            "input": "q1 tahun ini totalnya",
            "parsed": gemini_query_stub("q1 tahun ini totalnya", "2026-01-01", "2026-04-30"),
            "expect": {
                "intent": "query_summary",
                "action": "ask_clarification",
                "top_needs_clarification": True,
                "date_preset": "ambiguous_date_range",
            },
        },
        {
            "name": "validator drops query on edit draft",
            "input": "ganti kategori transport",
            "parsed": {
                "intent": "edit_draft",
                "action": "edit_draft",
                "currency": "IDR",
                "description": "ganti kategori transport",
                "category_hint": "Transport",
                "account_hint": "",
                "transaction_date": "2026-04-27",
                "transactions": [],
                "query": {
                    "metric": "expense_total",
                    "type": "expense",
                    "date_range": {
                        "raw_text": "",
                        "preset": "today_default",
                        "start_date": "2026-04-27",
                        "end_date": "2026-04-27",
                        "confidence": 0.35,
                    },
                    "needs_clarification": True,
                    "clarification_prompt": "bad query pollution",
                },
                "confidence": 0.9,
                "missing_fields": [],
            },
            "expect": {
                "intent": "edit_draft",
                "action": "edit_draft",
                "top_needs_clarification": False,
            },
        },
        {
            "name": "validator rejects typo month with totalnya",
            "input": "fbruari tahun kmrn totalnya",
            "parsed": gemini_query_stub("fbruari tahun kmrn totalnya", "2026-04-29", "2026-04-29"),
            "expect": {
                "intent": "query_summary",
                "action": "ask_clarification",
                "top_needs_clarification": True,
                "date_preset": "unknown_month_date",
            },
        },
        {
            "name": "validator upgrades delete wording to edit draft",
            "input": "hapus yang pertama",
            "parsed": {
                "intent": "unknown",
                "action": "none",
                "currency": "IDR",
                "description": "hapus yang pertama",
                "category_hint": "",
                "account_hint": "",
                "transaction_date": "2026-04-27",
                "transactions": [],
                "confidence": 0,
                "missing_fields": [],
                "_source_text": "hapus yang pertama",
            },
            "expect": {
                "intent": "edit_draft",
                "action": "edit_draft",
            },
        },
        {
            "name": "validator infers second item amount edit",
            "input": "yang kedua harusnya 90k",
            "parsed": {
                "intent": "edit_draft",
                "action": "edit_draft",
                "amount": 90000,
                "currency": "IDR",
                "description": "",
                "category_hint": "",
                "account_hint": "",
                "transaction_date": "2026-04-27",
                "transactions": [],
                "confidence": 0.9,
                "missing_fields": [],
                "_source_text": "yang kedua harusnya 90k",
            },
            "expect": {
                "intent": "edit_draft",
                "action": "edit_draft",
                "edit_field": "amount",
                "edit_target_item_index": 2,
                "edit_amount": 90000,
            },
        },
        {
            "name": "validator infers numeric item amount edit",
            "input": "yang 1 harusnya 30k",
            "parsed": {
                "intent": "edit_draft",
                "action": "edit_draft",
                "amount": 30000,
                "currency": "IDR",
                "description": "",
                "category_hint": "",
                "account_hint": "",
                "transaction_date": "2026-04-27",
                "transactions": [],
                "confidence": 0.9,
                "missing_fields": [],
                "_source_text": "yang 1 harusnya 30k",
            },
            "expect": {
                "intent": "edit_draft",
                "action": "edit_draft",
                "edit_field": "amount",
                "edit_target_item_index": 1,
                "edit_amount": 30000,
            },
        },
        {
            "name": "validator repairs zero target item index",
            "input": "1 harusnya 30k",
            "parsed": {
                "intent": "edit_draft",
                "action": "edit_draft",
                "amount": 30000,
                "currency": "IDR",
                "description": "",
                "category_hint": "",
                "account_hint": "",
                "transaction_date": "2026-04-27",
                "transactions": [],
                "edit": {
                    "target_item_index": 0,
                    "field": "amount",
                    "amount": 30000
                },
                "confidence": 0.9,
                "missing_fields": [],
                "_source_text": "1 harusnya 30k",
            },
            "expect": {
                "intent": "edit_draft",
                "action": "edit_draft",
                "edit_field": "amount",
                "edit_target_item_index": 1,
                "edit_amount": 30000,
            },
        },
        {
            "name": "validator clears create income clarification with amount",
            "input": "income 2jt freelance",
            "parsed": {
                "intent": "create_income",
                "action": "ask_clarification",
                "needs_clarification": True,
                "clarification_prompt": "unneeded",
                "amount": 2000000,
                "currency": "IDR",
                "description": "freelance",
                "category_hint": "Income",
                "account_hint": "",
                "transaction_date": "2026-04-27",
                "transactions": [],
                "confidence": 0.8,
                "missing_fields": [],
                "_source_text": "income 2jt freelance",
            },
            "expect": {
                "intent": "create_income",
                "action": "create_draft",
                "top_needs_clarification": False,
                "needs_confirmation": True,
                "amount": 2000000,
            },
        },
    ]


def receipt_cases() -> list[dict[str, Any]]:
    return [
        {
            "name": "receipt total belanja creates draft",
            "input": "receipt image",
            "ocr": {
                "lines": [
                    {"text": "TOKO NGAMPELSARI", "confidence": 0.96},
                    {"text": "AIR MINERAL", "confidence": 0.95},
                    {"text": "TOTAL BELANJA :", "confidence": 0.99},
                    {"text": "28.200", "confidence": 0.99},
                    {"text": "TUNAI :", "confidence": 0.98},
                    {"text": "50.000", "confidence": 0.98},
                    {"text": "KEMBALI :", "confidence": 0.98},
                    {"text": "21.800", "confidence": 0.98},
                ]
            },
            "expect": {"action": "create_draft", "amount": 28200, "needs_confirmation": True},
        },
        {
            "name": "atm withdrawal is not receipt",
            "input": "atm slip image",
            "ocr": {
                "lines": [
                    {"text": "ATM LINK", "confidence": 0.96},
                    {"text": "PENARIKAN", "confidence": 0.96},
                    {"text": "RP.", "confidence": 0.96},
                    {"text": "100.000,00", "confidence": 0.96},
                    {"text": "SALDO", "confidence": 0.96},
                    {"text": "RP.", "confidence": 0.96},
                    {"text": "1.724.000,00", "confidence": 0.96},
                    {"text": "NO KARTU", "confidence": 0.96},
                ]
            },
            "expect": {"action": "none", "needs_confirmation": False},
        },
    ]


def gemini_query_stub(source_text: str, start_date: str, end_date: str) -> dict[str, Any]:
    return {
        "intent": "query_summary",
        "action": "run_query",
        "currency": "IDR",
        "description": source_text,
        "category_hint": "",
        "account_hint": "",
        "transaction_date": "2026-04-27",
        "transactions": [],
        "query": {
            "metric": "expense_total",
            "type": "expense",
            "date_range": {
                "raw_text": source_text,
                "preset": "gemini_date_range",
                "start_date": start_date,
                "end_date": end_date,
                "confidence": 0.9,
            },
            "needs_clarification": False,
            "clarification_prompt": "",
        },
        "confidence": 0.9,
        "missing_fields": [],
        "_source_text": source_text,
    }


def today():
    return FIXED_TODAY.date()


def parse_date(value: Any):
    if not value:
        raise AssertionError("date value is empty")
    return datetime.strptime(str(value), "%Y-%m-%d").date()


def add_months(value, months: int):
    month_index = value.month - 1 + months
    year = value.year + month_index // 12
    month = month_index % 12 + 1
    return value.replace(year=year, month=month)


def format_failure(case: dict[str, Any], parsed: dict[str, Any], errors: list[str]) -> str:
    return "\n".join(
        [
            f"FAIL: {case['name']}",
            f"input: {case['input']!r}",
            "errors:",
            *[f"  - {error}" for error in errors],
            "parsed:",
            json.dumps(parsed, indent=2, ensure_ascii=False),
        ]
    )


if __name__ == "__main__":
    sys.exit(main())
