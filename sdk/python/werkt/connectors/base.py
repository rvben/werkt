from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any, Mapping, Protocol
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlparse
from urllib.request import Request, urlopen


class ConnectorError(RuntimeError):
    """Safe external-operation failure that omits credentials and request URLs."""


@dataclass(frozen=True)
class ConnectorField:
    id: str
    label: str
    secret: bool = False
    required: bool = True


@dataclass(frozen=True)
class ConnectorSpec:
    id: str
    title: str
    description: str
    hosts: tuple[str, ...]
    credentials: tuple[ConnectorField, ...] = ()
    configuration: tuple[ConnectorField, ...] = ()


@dataclass(frozen=True)
class HTTPResponse:
    status: int
    headers: Mapping[str, str]
    body: bytes

    def json(self, label: str = "HTTP response") -> Any:
        try:
            return json.loads(self.body)
        except (UnicodeDecodeError, json.JSONDecodeError) as error:
            raise ConnectorError(f"{label} returned invalid JSON") from error


@dataclass(frozen=True)
class DownloadRequest:
    """An allowlisted download URL plus credentials kept out of its query."""

    url: str
    headers: Mapping[str, str]


class Transport(Protocol):
    def request(self, method: str, url: str, *, headers: Mapping[str, str] | None = None, body: bytes | None = None, timeout: float = 30) -> HTTPResponse: ...


class UrllibTransport:
    def __init__(self, allowed_hosts: set[str] | frozenset[str] | None = None) -> None:
        self.allowed_hosts = frozenset(host.lower() for host in (allowed_hosts or ()))

    def request(self, method: str, url: str, *, headers: Mapping[str, str] | None = None, body: bytes | None = None, timeout: float = 30) -> HTTPResponse:
        parsed = urlparse(url)
        if parsed.scheme != "https" or not parsed.hostname:
            raise ConnectorError("connector URL must use HTTPS with an exact hostname")
        if self.allowed_hosts and parsed.hostname.lower() not in self.allowed_hosts:
            raise ConnectorError("connector destination is not allowlisted")
        try:
            with urlopen(Request(url, method=method, data=body, headers=dict(headers or {})), timeout=timeout) as response:
                return HTTPResponse(response.status, dict(response.headers.items()), response.read())
        except HTTPError as error:
            raise ConnectorError(f"connector request returned HTTP {error.code}") from error
        except (URLError, TimeoutError, OSError) as error:
            raise ConnectorError("connector request failed") from error


class HTTPClient:
    def __init__(self, transport: Transport | None = None) -> None:
        self.transport = transport or UrllibTransport()

    def request(self, method: str, url: str, *, headers: Mapping[str, str] | None = None, body: bytes | None = None, timeout: float = 30, expected: range = range(200, 300)) -> HTTPResponse:
        response = self.transport.request(method, url, headers=headers, body=body, timeout=timeout)
        if response.status not in expected:
            raise ConnectorError(f"connector request returned HTTP {response.status}")
        return response

    def json(self, method: str, url: str, *, headers: Mapping[str, str] | None = None, value: Any = None, form: Mapping[str, str] | None = None, timeout: float = 30) -> Any:
        request_headers = dict(headers or {})
        body = None
        if value is not None:
            body = json.dumps(value, separators=(",", ":")).encode()
            request_headers["Content-Type"] = "application/json"
        elif form is not None:
            body = urlencode(form).encode()
            request_headers["Content-Type"] = "application/x-www-form-urlencoded"
        return self.request(method, url, headers=request_headers, body=body, timeout=timeout).json()
