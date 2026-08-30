from __future__ import annotations

import base64
import time
from typing import Mapping

from .base import ConnectorError, HTTPClient


class OAuth2ClientCredentials:
    def __init__(self, client: HTTPClient, token_url: str, client_id: str, client_secret: str, *, fields: Mapping[str, str] | None = None, basic_auth: bool = True) -> None:
        self.client = client
        self.token_url = token_url
        self.client_id = client_id
        self.client_secret = client_secret
        self.fields = dict(fields or {})
        self.basic_auth = basic_auth
        self._token = ""
        self._expires_at = 0.0

    def token(self) -> str:
        if self._token and time.time() < self._expires_at - 60:
            return self._token
        fields = {"grant_type": "client_credentials", **self.fields}
        headers: dict[str, str] = {}
        if self.basic_auth:
            encoded = base64.b64encode(f"{self.client_id}:{self.client_secret}".encode()).decode()
            headers["Authorization"] = f"Basic {encoded}"
        else:
            fields.update({"client_id": self.client_id, "client_secret": self.client_secret})
        value = self.client.json("POST", self.token_url, headers=headers, form=fields)
        if not isinstance(value, dict) or not isinstance(value.get("access_token"), str):
            raise ConnectorError("OAuth2 response omitted access_token")
        self._token = value["access_token"]
        self._expires_at = time.time() + int(value.get("expires_in", 3600))
        return self._token
