from __future__ import annotations

from dataclasses import dataclass
from typing import Mapping

from .base import HTTPResponse


@dataclass(frozen=True)
class RecordedRequest:
    method: str
    url: str
    headers: Mapping[str, str]
    body: bytes | None
    timeout: float

    @property
    def host(self) -> str:
        from urllib.parse import urlparse
        return urlparse(self.url).hostname or ""


class ScriptedTransport:
    """Deterministic connector transport; no socket can be opened accidentally."""

    def __init__(self, responses: list[HTTPResponse]) -> None:
        self.responses = list(responses)
        self.requests: list[RecordedRequest] = []

    def request(self, method: str, url: str, *, headers=None, body=None, timeout: float = 30) -> HTTPResponse:
        self.requests.append(RecordedRequest(method, url, dict(headers or {}), body, timeout))
        if not self.responses:
            raise AssertionError("connector made an unexpected request")
        return self.responses.pop(0)

    def assert_finished(self) -> None:
        if self.responses:
            raise AssertionError(f"{len(self.responses)} scripted connector responses were not consumed")
