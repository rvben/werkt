from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from typing import Any, Iterable, MutableMapping

from .runtime import Context


class StaleContinuation(RuntimeError):
    """The durable step already advanced and should succeed as a no-op."""


@dataclass
class Job:
    key: str
    value: MutableMapping[str, Any]

    @property
    def state(self) -> str:
        return str(self.value.get("state", ""))

    def require(self, expected: str | Iterable[str]) -> Job:
        states = {expected} if isinstance(expected, str) else set(expected)
        if self.state not in states:
            raise StaleContinuation(f"job {self.key} is {self.state}, expected {sorted(states)}")
        return self

    def transition(self, target: str, now: datetime, **updates: Any) -> Job:
        self.value.update(updates)
        self.value["state"] = target
        self.value["updatedAt"] = now.isoformat()
        return self


class DurableWorkflow:
    """Small transactional job registry backed by ``Context.state``."""

    def __init__(self, context: Context, namespace: str = "jobs", limit: int = 128) -> None:
        self.context = context
        self.namespace = namespace
        self.limit = limit
        raw = context.state.get(namespace)
        self.jobs: dict[str, MutableMapping[str, Any]] = {
            str(key): value for key, value in raw.items() if isinstance(value, dict)
        } if isinstance(raw, dict) else {}

    def get(self, key: str) -> Job | None:
        value = self.jobs.get(key)
        return Job(key, value) if value is not None else None

    def require(self, key: str, expected: str | Iterable[str] | None = None) -> Job:
        job = self.get(key)
        if job is None:
            raise KeyError(f"job {key} does not exist")
        return job.require(expected) if expected is not None else job

    def start(self, key: str, state: str, now: datetime, **values: Any) -> tuple[Job, bool]:
        existing = self.get(key)
        if existing is not None:
            return existing, False
        value: MutableMapping[str, Any] = {**values, "state": state, "createdAt": now.isoformat(), "updatedAt": now.isoformat()}
        self.jobs[key] = value
        self._commit()
        return Job(key, value), True

    def defer(self, job: Job, step: str, until: datetime, data: dict[str, Any] | None = None) -> None:
        payload = {"jobId": job.key, "step": step, **(data or {})}
        self.context.defer(key=f"{job.key}.{step}", until=until, data=payload)

    def commit(self) -> None:
        self._commit()

    def _commit(self) -> None:
        ordered = sorted(self.jobs.items(), key=lambda item: str(item[1].get("updatedAt", "")), reverse=True)
        self.jobs = dict(ordered[: self.limit])
        self.context.state[self.namespace] = self.jobs
