"""Small Python adapter for the Werkt process protocol."""

from __future__ import annotations

import json
import os
from dataclasses import dataclass
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

    @classmethod
    def from_environment(cls) -> "Context":
        return cls(
            automation_id=os.environ["WERKT_AUTOMATION_ID"],
            revision_id=os.environ["WERKT_REVISION_ID"],
            run_id=os.environ["WERKT_RUN_ID"],
        )

    def log(self, message: str, **fields: Any) -> None:
        print(json.dumps({"message": message, **fields}, separators=(",", ":")))


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
