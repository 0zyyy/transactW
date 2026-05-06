import json
from typing import Any

from providers import LLMProvider, ProviderResult


def verify_receipt_with_llm(ocr_result: dict[str, Any], candidates: dict[str, Any], provider: LLMProvider) -> ProviderResult:
    if not provider.enabled():
        raise RuntimeError(f"{provider.name} verifier is disabled")
    prompt = f"""
You verify receipt OCR output for a finance bot. The OCR text was extracted by docTR.
Return only JSON.

Rules:
- Do not read an image. Use only the OCR text and local candidates below.
- If this is an ATM withdrawal, balance slip, random photo, meme, or non-purchase proof, set is_receipt false.
- Prefer selected_total from local total_candidates. Do not invent amounts not visible in OCR text.
- Prefer purchase totals such as TOTAL BELANJA, TOTAL BAYAR, GRAND TOTAL, JUMLAH.
- Reject cash tendered, saldo, tunai, kembalian/change, tax/ppn, discount, subtotal as selected_total.
- If receipt-like but total is unclear, set needs_clarification true.
- Keep line_items only when supported by OCR text.

Expected JSON:
{{
  "is_receipt": true,
  "selected_total": 28200,
  "selected_total_label": "TOTAL BELANJA",
  "merchant": "",
  "date": "YYYY-MM-DD or empty",
  "currency": "IDR",
  "category_hint": "Makan & Minum|Transport|Belanja Harian|Tagihan|Hiburan|Kesehatan|Pendidikan|Income|Transfer|Lainnya|",
  "line_items": [{{"name": "", "amount": 0, "confidence": 0.0}}],
  "confidence": 0.0,
  "needs_clarification": false,
  "clarification_prompt": "",
  "payment_method": "",
  "notes": ""
}}

OCR text:
{ocr_result.get("text", "")}

Local candidates:
{json.dumps(candidates, ensure_ascii=False)}
""".strip()
    return provider.generate_json(prompt, temperature=0.0)
