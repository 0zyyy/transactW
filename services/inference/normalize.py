import re
from typing import Any

from config import CATEGORY_ALIASES, CATEGORY_HINTS


def normalize_category(value: Any) -> str:
    category = str(value or "").strip()
    if category in CATEGORY_HINTS:
        return category
    return CATEGORY_ALIASES.get(category.lower(), "Lainnya" if category else "")


def parse_int_amount(value: Any) -> int | None:
    if value in ("", None):
        return None
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        return int(value)
    digits = re.sub(r"[^0-9]", "", str(value))
    if not digits:
        return None
    return int(digits)


def safe_float(value: Any, fallback: float) -> float:
    try:
        if value in (None, ""):
            return fallback
        return float(value)
    except (TypeError, ValueError):
        return fallback
