from __future__ import annotations

import base64
import json
import mimetypes
import uuid
from pathlib import Path
from typing import Any

from .base import ConnectorError, ConnectorField, ConnectorSpec, HTTPClient


class OpenAI:
    spec = ConnectorSpec("openai", "OpenAI", "Chat, vision, and audio transcription", ("api.openai.com",), credentials=(ConnectorField("api_key", "API key", secret=True),))
    def __init__(self, api_key: str, *, client: HTTPClient | None = None, base_url: str = "https://api.openai.com/v1") -> None:
        self.api_key = api_key
        self.client = client or HTTPClient()
        self.base_url = base_url.rstrip("/")

    @property
    def headers(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {self.api_key}"}

    def chat(self, *, model: str, messages: list[dict[str, Any]], temperature: float = 0, max_tokens: int = 500, json_mode: bool = False) -> str:
        payload: dict[str, Any] = {"model": model, "messages": messages, "temperature": temperature, "max_tokens": max_tokens}
        if json_mode:
            payload["response_format"] = {"type": "json_object"}
        value = self.client.json("POST", f"{self.base_url}/chat/completions", headers=self.headers, value=payload, timeout=60)
        try:
            content = value["choices"][0]["message"]["content"]
        except (KeyError, IndexError, TypeError) as error:
            raise ConnectorError("OpenAI response omitted message content") from error
        if not isinstance(content, str):
            raise ConnectorError("OpenAI response contained invalid message content")
        return content

    def vision(self, *, model: str, prompt: str, image: Path, detail: str = "high", json_mode: bool = True) -> str:
        encoded = base64.b64encode(image.read_bytes()).decode()
        mime = mimetypes.guess_type(image.name)[0] or "image/jpeg"
        return self.chat(model=model, json_mode=json_mode, messages=[{"role": "user", "content": [
            {"type": "text", "text": prompt},
            {"type": "image_url", "image_url": {"url": f"data:{mime};base64,{encoded}", "detail": detail}},
        ]}])

    def transcribe(self, audio: Path, *, model: str = "whisper-1", language: str | None = None) -> str:
        boundary = "werkt-" + uuid.uuid4().hex
        fields = [("model", model)] + ([("language", language)] if language else [])
        parts = [f"--{boundary}\r\nContent-Disposition: form-data; name=\"{key}\"\r\n\r\n{value}\r\n".encode() for key, value in fields]
        parts.extend([
            f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{audio.name}\"\r\nContent-Type: application/octet-stream\r\n\r\n".encode(),
            audio.read_bytes(), f"\r\n--{boundary}--\r\n".encode(),
        ])
        headers = {**self.headers, "Content-Type": f"multipart/form-data; boundary={boundary}"}
        value = self.client.request("POST", f"{self.base_url}/audio/transcriptions", headers=headers, body=b"".join(parts), timeout=180).json("OpenAI transcription")
        if not isinstance(value, dict) or not isinstance(value.get("text"), str):
            raise ConnectorError("OpenAI transcription response omitted text")
        return value["text"]
