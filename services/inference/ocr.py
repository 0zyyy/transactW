import base64
import os
import tempfile
from typing import Any

from config import DOCTR_DET_ARCH, DOCTR_RECO_ARCH


class OCRError(RuntimeError):
    pass


_DOCTR_MODEL: Any = None


def extract_text_with_doctr(image_base64: str) -> dict[str, Any]:
    try:
        from doctr.io import DocumentFile
    except Exception as exc:
        raise OCRError(f"docTR import failed: {exc}") from exc

    try:
        image_bytes = base64.b64decode(image_base64)
        temp_path = write_temp_image(image_bytes)
        document = DocumentFile.from_images(temp_path)
    except Exception as exc:
        raise OCRError(f"failed to decode image for OCR: {exc}") from exc

    try:
        result = doctr_model()(document).export()
    except Exception as exc:
        raise OCRError(f"docTR OCR failed: {exc}") from exc
    finally:
        if "temp_path" in locals():
            try:
                os.remove(temp_path)
            except OSError:
                pass

    lines: list[dict[str, Any]] = []
    for page in result.get("pages") or []:
        for block in page.get("blocks") or []:
            for line in block.get("lines") or []:
                words = line.get("words") or []
                text = " ".join(str(word.get("value") or "") for word in words).strip()
                if not text:
                    continue
                confidence = sum(float(word.get("confidence") or 0) for word in words) / len(words) if words else 0
                lines.append({"text": text, "confidence": confidence})

    full_text = "\n".join(line["text"] for line in lines)
    confidence = sum(float(line["confidence"] or 0) for line in lines) / len(lines) if lines else 0
    return {
        "engine": "doctr",
        "text": full_text,
        "lines": lines,
        "confidence": confidence,
    }


def doctr_model() -> Any:
    global _DOCTR_MODEL
    if _DOCTR_MODEL is None:
        try:
            from doctr.models import ocr_predictor

            _DOCTR_MODEL = ocr_predictor(det_arch=DOCTR_DET_ARCH, reco_arch=DOCTR_RECO_ARCH, pretrained=True)
        except Exception as exc:
            raise OCRError(f"docTR model load failed: {exc}") from exc
    return _DOCTR_MODEL


def write_temp_image(image_bytes: bytes) -> str:
    with tempfile.NamedTemporaryFile(delete=False, suffix=".jpg") as file:
        file.write(image_bytes)
        return file.name
