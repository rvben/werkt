from __future__ import annotations

import json
import os
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Any, Callable, Mapping, MutableMapping


def _object(path: Path, label: str) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise RuntimeError(f"{label} is unreadable") from error
    if not isinstance(value, dict):
        raise RuntimeError(f"{label} must be a JSON object")
    return value


@dataclass(frozen=True)
class Event:
    id: str
    occurred_at: str
    received_at: str
    trigger: Mapping[str, Any]
    data: Any
    metadata: Mapping[str, Any]

    @classmethod
    def from_dict(cls, value: Mapping[str, Any]) -> Event:
        return cls(
            id=str(value["id"]),
            occurred_at=str(value.get("occurredAt", "")),
            received_at=str(value.get("receivedAt", "")),
            trigger=value.get("trigger", {}),
            data=value.get("data"),
            metadata=value.get("metadata", {}),
        )

    @property
    def trigger_type(self) -> str:
        return str(self.trigger.get("type", ""))


@dataclass
class RunControl:
    defer: dict[str, Any] | None = None
    approval: dict[str, Any] | None = None

    def as_dict(self) -> dict[str, Any]:
        value: dict[str, Any] = {}
        if self.defer is not None:
            value["defer"] = self.defer
        if self.approval is not None:
            value["approval"] = self.approval
        return value


@dataclass
class Context:
    automation_id: str
    revision_id: str
    run_id: str
    control_path: Path
    state_path: Path | None = None
    result_path: Path | None = None
    state: MutableMapping[str, Any] = field(default_factory=dict)
    control: RunControl = field(default_factory=RunControl)

    @classmethod
    def from_environment(cls) -> Context:
        state_path = Path(os.environ["WERKT_STATE_PATH"]) if os.environ.get("WERKT_STATE_PATH") else None
        return cls(
            automation_id=os.environ["WERKT_AUTOMATION_ID"],
            revision_id=os.environ["WERKT_REVISION_ID"],
            run_id=os.environ["WERKT_RUN_ID"],
            control_path=Path(os.environ["WERKT_CONTROL_PATH"]),
            state_path=state_path,
            result_path=Path(os.environ["WERKT_RESULT_PATH"]),
            state=_object(state_path, "Werkt state") if state_path else {},
        )

    def log(self, message: str, **fields: Any) -> None:
        print(json.dumps({"message": message, **fields}, separators=(",", ":")))

    def defer(self, *, key: str, until: datetime | str, data: Any = None) -> None:
        timestamp = until.isoformat() if isinstance(until, datetime) else until
        self.control.defer = {"key": key, "until": timestamp, "data": {} if data is None else data}
        self._write_control()

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
        timestamp = expires_at.isoformat() if isinstance(expires_at, datetime) else expires_at
        self.control.approval = {
            "key": key,
            "title": title,
            "description": description,
            "expiresAt": timestamp,
            "fields": fields,
            "actions": actions,
        }
        self._write_control()

    def commit(self, result: Any) -> None:
        if self.state_path is not None:
            self.state_path.write_text(json.dumps(self.state, separators=(",", ":")), encoding="utf-8")
        self._write_control()
        if self.result_path is not None:
            self.result_path.write_text(json.dumps(result, separators=(",", ":")), encoding="utf-8")

    def _write_control(self) -> None:
        self.control_path.write_text(json.dumps(self.control.as_dict(), separators=(",", ":")), encoding="utf-8")


Handler = Callable[[Event, Context], Any]


def execute(handler: Handler) -> Any:
    event = Event.from_dict(_object(Path(os.environ["WERKT_EVENT_PATH"]), "Werkt event"))
    context = Context.from_environment()
    result = handler(event, context)
    context.commit(result)
    return result


def automation(handler: Handler) -> Handler:
    """Mark a function as an automation handler without changing its signature."""
    return handler
