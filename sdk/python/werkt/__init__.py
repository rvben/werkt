"""Small Python adapter for the Werkt process protocol."""

from __future__ import annotations

import json
import os
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any, Callable, Mapping


@dataclass(frozen=True)
class Event:
    id: str
    occurred_at: str
    received_at: str
    trigger: Mapping[str, Any]
    data: Any
    metadata: Mapping[str, Any]

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> "Event":
        return cls(
            id=str(value["id"]),
            occurred_at=str(value["occurredAt"]),
            received_at=str(value["receivedAt"]),
            trigger=value.get("trigger", {}),
            data=value.get("data"),
            metadata=value.get("metadata", {}),
        )


@dataclass(frozen=True)
class Context:
    automation_id: str
    revision_id: str
    run_id: str
    control_path: Path

    @classmethod
    def from_environment(cls) -> "Context":
        return cls(
            automation_id=os.environ["WERKT_AUTOMATION_ID"],
            revision_id=os.environ["WERKT_REVISION_ID"],
            run_id=os.environ["WERKT_RUN_ID"],
            control_path=Path(os.environ["WERKT_CONTROL_PATH"]),
        )

    def log(self, message: str, **fields: Any) -> None:
        print(json.dumps({"message": message, **fields}, separators=(",", ":")))

    def defer(self, *, key: str, until: datetime | str, data: Any = None) -> None:
        """Schedule one continuation after this run and its state commit succeed."""
        timestamp = until.isoformat() if isinstance(until, datetime) else until
        self._write_control({"defer": {"key": key, "until": timestamp, "data": data}})

    def request_approval(
        self,
        *,
        key: str,
        title: str,
        expires_at: datetime | str,
        fields: list[Mapping[str, Any]],
        actions: list[Mapping[str, Any]],
        description: str = "",
    ) -> None:
        """Pause for a typed operator decision and resume on its selected action."""
        timestamp = expires_at.isoformat() if isinstance(expires_at, datetime) else expires_at
        self._write_control(
            {
                "approval": {
                    "key": key,
                    "title": title,
                    "description": description,
                    "expiresAt": timestamp,
                    "fields": fields,
                    "actions": actions,
                }
            }
        )

    def _write_control(self, value: Mapping[str, Any]) -> None:
        self.control_path.write_text(json.dumps(value, separators=(",", ":")), encoding="utf-8")


Handler = Callable[[Event, Context], Any]


def execute(handler: Handler) -> None:
    event_path = Path(os.environ["WERKT_EVENT_PATH"])
    result_path = Path(os.environ["WERKT_RESULT_PATH"])
    event = Event.from_dict(json.loads(event_path.read_text(encoding="utf-8")))
    result = handler(event, Context.from_environment())
    result_path.write_text(json.dumps(result), encoding="utf-8")


def automation(handler: Handler) -> Handler:
    """Mark a function as an automation handler without wrapping its signature."""
    return handler
