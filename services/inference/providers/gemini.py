import json
import re
import urllib.error
import urllib.request
from typing import Any

from .base import LLMProvider, ProviderResult


class GeminiProvider(LLMProvider):
    name = "gemini"
    supports_text = True
    supports_vision = True
    supports_audio = True
    supports_response_schema = True

    def generate_json(self, prompt: str, schema: dict[str, Any] | None = None, temperature: float = 0.1) -> ProviderResult:
        generation_config: dict[str, Any] = {
            "temperature": temperature,
            "responseMimeType": "application/json",
        }
        if schema is not None:
            generation_config["responseSchema"] = schema
        payload = {
            "contents": [{"parts": [{"text": prompt}]}],
            "generationConfig": generation_config,
        }
        return ProviderResult(self.name, self.model, self._generate_json_payload(payload))

    def generate_vision_json(
        self,
        prompt: str,
        image_base64: str,
        mime_type: str,
        schema: dict[str, Any] | None = None,
        temperature: float = 0.1,
    ) -> ProviderResult:
        generation_config: dict[str, Any] = {
            "temperature": temperature,
            "responseMimeType": "application/json",
        }
        if schema is not None:
            generation_config["responseSchema"] = schema
        payload = {
            "contents": [
                {
                    "parts": [
                        {"text": prompt},
                        {"inline_data": {"mime_type": mime_type or "image/jpeg", "data": image_base64}},
                    ]
                }
            ],
            "generationConfig": generation_config,
        }
        return ProviderResult(self.name, self.model, self._generate_json_payload(payload))

    def generate_audio_json(
        self,
        prompt: str,
        audio_base64: str,
        mime_type: str,
        schema: dict[str, Any] | None = None,
        temperature: float = 0.0,
    ) -> ProviderResult:
        generation_config: dict[str, Any] = {
            "temperature": temperature,
            "responseMimeType": "application/json",
        }
        if schema is not None:
            generation_config["responseSchema"] = schema
        payload = {
            "contents": [
                {
                    "parts": [
                        {"text": prompt},
                        {"inline_data": {"mime_type": mime_type or "audio/ogg", "data": audio_base64}},
                    ]
                }
            ],
            "generationConfig": generation_config,
        }
        return ProviderResult(self.name, self.model, self._generate_json_payload(payload))

    def _generate_json_payload(self, payload: dict[str, Any]) -> dict[str, Any]:
        data = self._generate_content(payload)
        return json.loads(strip_json_fence(extract_text(data)))

    def _generate_content(self, payload: dict[str, Any]) -> dict[str, Any]:
        if not self.api_key:
            raise RuntimeError("Gemini provider is disabled")
        url = f"https://generativelanguage.googleapis.com/v1beta/models/{self.model}:generateContent?key={self.api_key}"
        req = urllib.request.Request(
            url,
            data=json.dumps(payload).encode("utf-8"),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            with urllib.request.urlopen(req, timeout=self.timeout_seconds) as resp:
                return json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")
            raise RuntimeError(f"gemini HTTP {exc.code}: {detail}") from exc


def extract_text(data: dict[str, Any]) -> str:
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
