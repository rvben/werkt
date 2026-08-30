from __future__ import annotations

import base64
from typing import Mapping

from .base import ConnectorField, ConnectorSpec, HTTPClient


class Ntfy:
    spec = ConnectorSpec(
        "ntfy", "ntfy", "Publish a notification to one configured topic", (),
        credentials=(ConnectorField("credential", "Basic credential", secret=True, required=False), ConnectorField("token", "Access token", secret=True, required=False)),
        configuration=(ConnectorField("topic_url", "Topic URL"),),
    )
    def __init__(self, topic_url: str, *, credential: str = "", token: str = "", client: HTTPClient | None = None) -> None:
        self.topic_url = topic_url
        self.credential = credential
        self.token = token
        self.client = client or HTTPClient()

    def publish(self, message: str, *, title: str = "", priority: str = "default", tags: str = "", headers: Mapping[str, str] | None = None) -> None:
        request_headers = {"Title": title, "Priority": priority, "Tags": tags, "Content-Type": "text/plain; charset=utf-8", **dict(headers or {})}
        if self.token:
            request_headers["Authorization"] = f"Bearer {self.token}"
        elif self.credential:
            request_headers["Authorization"] = "Basic " + base64.b64encode(self.credential.encode()).decode()
        self.client.request("POST", self.topic_url, headers=request_headers, body=message.encode())
