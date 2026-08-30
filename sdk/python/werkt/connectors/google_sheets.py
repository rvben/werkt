from __future__ import annotations

import base64
import json
import subprocess
import tempfile
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any
from urllib.parse import quote

from .base import ConnectorError, ConnectorField, ConnectorSpec, HTTPClient


@dataclass
class GoogleServiceAccount:
    client_email: str
    private_key: str
    client: HTTPClient
    token_uri: str = "https://oauth2.googleapis.com/token"
    _access_token: str = ""
    _expires_at: float = 0.0
    _scopes: tuple[str, ...] = ()

    @classmethod
    def from_json(cls, value: str, client: HTTPClient | None = None) -> GoogleServiceAccount:
        try:
            info = json.loads(value)
        except json.JSONDecodeError as error:
            raise ConnectorError("Google service account is invalid JSON") from error
        email, key = str(info.get("client_email", "")), str(info.get("private_key", ""))
        uri = str(info.get("token_uri", "https://oauth2.googleapis.com/token"))
        if not email or "BEGIN PRIVATE KEY" not in key or uri != "https://oauth2.googleapis.com/token":
            raise ConnectorError("Google service account is incomplete")
        return cls(email, key, client or HTTPClient(), uri)

    @staticmethod
    def _b64(value: bytes) -> str:
        return base64.urlsafe_b64encode(value).rstrip(b"=").decode()

    def token(self, scopes: list[str]) -> str:
        requested_scopes = tuple(sorted(set(scopes)))
        if not requested_scopes:
            raise ConnectorError("Google OAuth requires at least one scope")
        if self._access_token and requested_scopes == self._scopes and time.time() < self._expires_at - 60:
            return self._access_token
        now = int(time.time())
        header = self._b64(b'{"alg":"RS256","typ":"JWT"}')
        claims = self._b64(json.dumps({"iss": self.client_email, "scope": " ".join(requested_scopes), "aud": self.token_uri, "iat": now, "exp": now + 3600}, separators=(",", ":")).encode())
        unsigned = f"{header}.{claims}".encode()
        with tempfile.TemporaryDirectory(prefix="werkt-google-auth-") as directory:
            key = Path(directory) / "key.pem"
            key.write_text(self.private_key, encoding="utf-8")
            key.chmod(0o600)
            try:
                signed = subprocess.run(["openssl", "dgst", "-sha256", "-sign", str(key)], input=unsigned, capture_output=True, timeout=15, check=False)
            except (OSError, subprocess.TimeoutExpired) as error:
                raise ConnectorError("Google service account signing failed") from error
        if signed.returncode != 0 or not signed.stdout:
            raise ConnectorError("Google service account signing failed")
        assertion = f"{header}.{claims}.{self._b64(signed.stdout)}"
        response = self.client.json("POST", self.token_uri, form={"grant_type": "urn:ietf:params:oauth:grant-type:jwt-bearer", "assertion": assertion})
        if not isinstance(response, dict) or not isinstance(response.get("access_token"), str):
            raise ConnectorError("Google OAuth response omitted access_token")
        self._access_token = response["access_token"]
        self._expires_at = time.time() + int(response.get("expires_in", 3600))
        self._scopes = requested_scopes
        return self._access_token


class GoogleSheets:
    SCOPE = "https://www.googleapis.com/auth/spreadsheets"
    spec = ConnectorSpec(
        "google-sheets", "Google Sheets", "Read, update, and clear spreadsheet ranges",
        ("oauth2.googleapis.com", "sheets.googleapis.com"),
        credentials=(ConnectorField("service_account_json", "Service account JSON", secret=True),),
    )

    def __init__(self, account: GoogleServiceAccount, *, client: HTTPClient | None = None) -> None:
        self.account = account
        self.client = client or account.client

    def _headers(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {self.account.token([self.SCOPE])}"}

    def values(self, spreadsheet_id: str, range_name: str) -> list[list[Any]]:
        value = self.client.json("GET", f"https://sheets.googleapis.com/v4/spreadsheets/{spreadsheet_id}/values/{quote(range_name, safe='')}", headers=self._headers())
        rows = value.get("values", []) if isinstance(value, dict) else None
        if not isinstance(rows, list):
            raise ConnectorError("Google Sheets response contained invalid values")
        return rows

    def update(self, spreadsheet_id: str, range_name: str, values: list[list[Any]]) -> None:
        self.client.json("PUT", f"https://sheets.googleapis.com/v4/spreadsheets/{spreadsheet_id}/values/{quote(range_name, safe='')}?valueInputOption=RAW", headers=self._headers(), value={"range": range_name, "majorDimension": "ROWS", "values": values})

    def clear(self, spreadsheet_id: str, range_name: str) -> None:
        self.client.json("POST", f"https://sheets.googleapis.com/v4/spreadsheets/{spreadsheet_id}/values/{quote(range_name, safe='')}:clear", headers=self._headers(), value={})
