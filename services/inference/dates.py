import re
from datetime import date, datetime, timedelta
from typing import Any


def normalize_date_range(text: str, today: date | None = None) -> dict[str, Any]:
    if today is None:
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
