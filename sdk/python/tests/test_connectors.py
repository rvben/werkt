from __future__ import annotations

import json
import unittest
from types import SimpleNamespace
from unittest.mock import patch

from werkt.connectors import BUILTIN_CONNECTORS, ConnectorError, GoogleServiceAccount, HTTPClient, HTTPResponse, Ntfy, OAuth2ClientCredentials, OpenAI, Zoom
from werkt.connectors.testing import ScriptedTransport


def response(value, status: int = 200) -> HTTPResponse:
    return HTTPResponse(status, {"Content-Type": "application/json"}, json.dumps(value).encode())


class ConnectorContractTest(unittest.TestCase):
    def test_builtin_connector_specs_have_unique_ids_and_explicit_credentials(self) -> None:
        self.assertEqual(set(BUILTIN_CONNECTORS), {"zoom", "openai", "google-sheets", "ntfy"})
        self.assertTrue(BUILTIN_CONNECTORS["zoom"].credentials[2].secret)
        self.assertIn("sheets.googleapis.com", BUILTIN_CONNECTORS["google-sheets"].hosts)

    def test_oauth_client_credentials_caches_token_and_never_uses_query_credentials(self) -> None:
        transport = ScriptedTransport([response({"access_token": "token", "expires_in": 3600})])
        oauth = OAuth2ClientCredentials(HTTPClient(transport), "https://auth.example/token", "client", "secret")
        self.assertEqual(oauth.token(), "token")
        self.assertEqual(oauth.token(), "token")
        self.assertEqual(len(transport.requests), 1)
        request = transport.requests[0]
        self.assertNotIn("secret", request.url)
        self.assertTrue(request.headers["Authorization"].startswith("Basic "))
        transport.assert_finished()

    def test_google_service_account_caches_tokens_per_scope_set(self) -> None:
        transport = ScriptedTransport([
            response({"access_token": "sheets-token", "expires_in": 3600}),
            response({"access_token": "drive-token", "expires_in": 3600}),
        ])
        account = GoogleServiceAccount("service@example.test", "mock-signing-material", HTTPClient(transport))
        signed = SimpleNamespace(returncode=0, stdout=b"signature")
        with patch("werkt.connectors.google_sheets.subprocess.run", return_value=signed):
            self.assertEqual(account.token(["scope:sheets"]), "sheets-token")
            self.assertEqual(account.token(["scope:sheets"]), "sheets-token")
            self.assertEqual(account.token(["scope:drive"]), "drive-token")
        self.assertEqual(len(transport.requests), 2)
        transport.assert_finished()

    def test_zoom_normalizes_recording_api_and_keeps_download_credentials_out_of_url(self) -> None:
        transport = ScriptedTransport([
            response({"access_token": "zoom-token", "expires_in": 3600}),
            response({"uuid": "/uuid", "recording_files": []}),
        ])
        zoom = Zoom("account", "client", "secret", allowed_download_hosts={"us02web.zoom.us"}, client=HTTPClient(transport))
        self.assertEqual(zoom.recording("/uuid")["uuid"], "/uuid")
        self.assertIn("%252Fuuid", transport.requests[1].url)
        download = zoom.download_request("https://us02web.zoom.us/rec/archive/download/id?existing=value")
        self.assertEqual(download.url, "https://us02web.zoom.us/rec/archive/download/id?existing=value")
        self.assertEqual(download.headers, {"Authorization": "Bearer zoom-token"})
        self.assertNotIn("zoom-token", download.url)
        with self.assertRaises(ConnectorError):
            zoom.download_request("https://attacker.example/recording")

    def test_ntfy_supports_basic_auth_without_embedding_it_in_url_or_body(self) -> None:
        transport = ScriptedTransport([HTTPResponse(200, {}, b"")])
        ntfy = Ntfy("https://ntfy.example/topic", credential="user:password", client=HTTPClient(transport))
        ntfy.publish("body", title="Title", tags="test")
        request = transport.requests[0]
        self.assertEqual(request.body, b"body")
        self.assertNotIn("password", request.url)
        self.assertTrue(request.headers["Authorization"].startswith("Basic "))

    def test_openai_transcription_accepts_a_workload_specific_timeout(self) -> None:
        transport = ScriptedTransport([response({"text": "Psalm 23"})])
        openai = OpenAI("openai-token", client=HTTPClient(transport))
        audio = SimpleNamespace(name="opening.m4a", read_bytes=lambda: b"audio")
        self.assertEqual(openai.transcribe(audio, model="gpt-transcribe", languages=["nl"], timeout=360), "Psalm 23")
        self.assertEqual(transport.requests[0].timeout, 360)
        self.assertIn(b'name="languages[]"\r\n\r\nnl\r\n', transport.requests[0].body)
        self.assertNotIn(b'name="language"', transport.requests[0].body)
        self.assertIn(b"Content-Type: audio/mp4", transport.requests[0].body)

    def test_openai_transcription_supports_text_delta_streams(self) -> None:
        body = b'data: {"type":"transcript.text.delta","delta":"Psalm "}\n\ndata: {"type":"transcript.text.delta","delta":"23"}\n\ndata: [DONE]\n\n'
        transport = ScriptedTransport([HTTPResponse(200, {"Content-Type": "text/event-stream"}, body)])
        openai = OpenAI("openai-token", client=HTTPClient(transport))
        audio = SimpleNamespace(name="opening.m4a", read_bytes=lambda: b"audio")
        self.assertEqual(openai.transcribe(audio, model="gpt-transcribe", languages=["nl"], stream=True), "Psalm 23")
        self.assertIn(b'name="stream"\r\n\r\ntrue\r\n', transport.requests[0].body)

    def test_openai_transcription_rejects_conflicting_language_hints(self) -> None:
        openai = OpenAI("openai-token", client=HTTPClient(ScriptedTransport([])))
        audio = SimpleNamespace(name="opening.m4a", read_bytes=lambda: b"audio")
        with self.assertRaisesRegex(ConnectorError, "language or languages"):
            openai.transcribe(audio, language="nl", languages=["nl"])

    def test_http_client_fails_closed_on_unexpected_status(self) -> None:
        client = HTTPClient(ScriptedTransport([response({"error": "no"}, status=503)]))
        with self.assertRaisesRegex(ConnectorError, "HTTP 503"):
            client.json("GET", "https://api.example/value")


if __name__ == "__main__":
    unittest.main()
