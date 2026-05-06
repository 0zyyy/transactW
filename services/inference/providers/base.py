from dataclasses import dataclass
from typing import Any


@dataclass(frozen=True)
class ProviderResult:
    provider: str
    model: str
    data: dict[str, Any]


class LLMProvider:
    name = ""
    supports_text = False
    supports_vision = False
    supports_response_schema = False

    def __init__(self, api_key: str, model: str, timeout_seconds: int) -> None:
        self.api_key = api_key
        self.model = model
        self.timeout_seconds = timeout_seconds

    def enabled(self) -> bool:
        return bool(self.api_key)

    def generate_json(self, prompt: str, schema: dict[str, Any] | None = None, temperature: float = 0.1) -> ProviderResult:
        raise NotImplementedError

    def generate_vision_json(
        self,
        prompt: str,
        image_base64: str,
        mime_type: str,
        schema: dict[str, Any] | None = None,
        temperature: float = 0.1,
    ) -> ProviderResult:
        raise NotImplementedError
