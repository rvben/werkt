from __future__ import annotations

from typing import Any
from urllib.parse import quote, urlencode, urlparse, urlunparse

from .base import ConnectorError, ConnectorField, ConnectorSpec, HTTPClient
from .oauth2 import OAuth2ClientCredentials


class Zoom:
    spec = ConnectorSpec(
        "zoom", "Zoom", "Server-to-Server OAuth recording access", ("zoom.us", "api.zoom.us"),
        credentials=(
            ConnectorField("account_id", "Account ID", secret=True),
            ConnectorField("client_id", "Client ID", secret=True),
            ConnectorField("client_secret", "Client secret", secret=True),
        ),
        configuration=(ConnectorField("download_hosts", "Exact recording download hosts"),),
    )
    def __init__(self, account_id: str, client_id: str, client_secret: str, *, allowed_download_hosts: set[str] | frozenset[str], client: HTTPClient | None = None) -> None:
        self.client = client or HTTPClient()
        self.allowed_download_hosts = frozenset(host.lower() for host in allowed_download_hosts)
        self.oauth = OAuth2ClientCredentials(self.client, "https://zoom.us/oauth/token", client_id, client_secret, fields={"grant_type": "account_credentials", "account_id": account_id})

    def recording(self, recording_uuid: str) -> dict[str, Any]:
        encoded = quote(quote(recording_uuid, safe=""), safe="")
        value = self.client.json("GET", f"https://api.zoom.us/v2/meetings/{encoded}/recordings", headers={"Authorization": f"Bearer {self.oauth.token()}"})
        if not isinstance(value, dict):
            raise ConnectorError("Zoom recordings response was invalid")
        return value

    def authenticated_download_url(self, url: str) -> str:
        parsed = urlparse(url)
        if parsed.scheme != "https" or (parsed.hostname or "").lower() not in self.allowed_download_hosts:
            raise ConnectorError("Zoom download destination is not allowlisted")
        query = parsed.query + ("&" if parsed.query else "") + urlencode({"access_token": self.oauth.token()})
        return urlunparse(parsed._replace(query=query))
