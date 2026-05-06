from .base import LLMProvider, ProviderResult
from .gemini import GeminiProvider

PROVIDERS = {
    "gemini": GeminiProvider,
}


def create_provider(name: str, api_key: str, model: str, timeout_seconds: int) -> LLMProvider:
    normalized = (name or "gemini").strip().lower()
    provider_type = PROVIDERS.get(normalized)
    if provider_type is None:
        raise RuntimeError(f"Unsupported inference provider: {name}")
    return provider_type(api_key, model, timeout_seconds)


__all__ = ["GeminiProvider", "LLMProvider", "ProviderResult", "create_provider"]
