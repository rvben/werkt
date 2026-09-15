from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any, Mapping, Protocol
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlparse
from urllib.request import Request, urlopen


# Who a connector request is from, for whoever has to answer it. Sending
# nothing leaves urllib to name Python and its own version, which edges in
# front of ordinary websites refuse: Cloudflare answers that agent with 403
# while serving the same URL to any client that says who it is, and a connector
# reading a public page then fails in a way that is indistinguishable from the
# page not being there yet.
#
# It names the platform and nothing else. An automation id is free text a
# person chose, so it can carry a name, a customer or a project; disclosing it
# to every destination would tell each of them something about the operator
# that answering the request never required. An automation with a host that
# expects a particular name passes that name itself.
USER_AGENT_PRODUCT = "Werkt-Automation/1.0"


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
    def __init__(self, allowed_hosts: set[str] | frozenset[str] | None = None, *, user_agent: str | None = None) -> None:
        self.allowed_hosts = frozenset(host.lower() for host in (allowed_hosts or ()))
        self.user_agent = user_agent

    def _headers(self, headers: Mapping[str, str] | None) -> dict[str, str]:
        """The caller's headers, named as Werkt unless the caller named itself.

        Matched without regard to case, because a header name is
        case-insensitive: urllib folds `user-agent` and `User-Agent` onto one
        key, so a default added beside a lowercase one silently replaces the
        agent a connector chose for a host with its own expectations, and a
        transport that does not fold would send two.
        An agent given for the whole transport is used as given, empty
        included: a caller spelling out an empty agent is asking to send one,
        and falling back to the default there would be the one way to ask for
        something the transport silently refuses.
        """
        merged = dict(headers or {})
        if not any(name.lower() == "user-agent" for name in merged):
            merged["User-Agent"] = USER_AGENT_PRODUCT if self.user_agent is None else self.user_agent
        return merged

    def request(self, method: str, url: str, *, headers: Mapping[str, str] | None = None, body: bytes | None = None, timeout: float = 30) -> HTTPResponse:
        parsed = urlparse(url)
        if parsed.scheme != "https" or not parsed.hostname:
            raise ConnectorError("connector URL must use HTTPS with an exact hostname")
        if self.allowed_hosts and parsed.hostname.lower() not in self.allowed_hosts:
            raise ConnectorError("connector destination is not allowlisted")
        try:
            with urlopen(Request(url, method=method, data=body, headers=self._headers(headers)), timeout=timeout) as response:
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
