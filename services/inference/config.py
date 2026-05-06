import os


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
OCR_ENGINE = os.getenv("OCR_ENGINE", "doctr")
OCR_GEMINI_VERIFY = os.getenv("OCR_GEMINI_VERIFY", "true").lower() == "true"
OCR_ALLOW_GEMINI_VISION_FALLBACK = os.getenv("OCR_ALLOW_GEMINI_VISION_FALLBACK", "false").lower() == "true"
OCR_TOTAL_DRAFT_CONFIDENCE_THRESHOLD = float(os.getenv("OCR_TOTAL_DRAFT_CONFIDENCE_THRESHOLD", "0.45"))
DOCTR_DET_ARCH = os.getenv("DOCTR_DET_ARCH", "fast_base")
DOCTR_RECO_ARCH = os.getenv("DOCTR_RECO_ARCH", "crnn_vgg16_bn")

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
