from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from typing import Any, Iterable, MutableMapping

from .runtime import Context


class StaleContinuation(RuntimeError):
    """The durable step already advanced and should succeed as a no-op."""


class WorkflowCapacityError(RuntimeError):
    """A new job would exceed capacity; all existing records are preserved."""


def _require_name(value: str, label: str) -> None:
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{label} must be a non-empty string")


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
    """Bounded job registry backed by ``Context.state``, without implicit eviction."""

    def __init__(self, context: Context, namespace: str = "jobs", limit: int = 128) -> None:
        _require_name(namespace, "workflow namespace")
        if type(limit) is not int or limit <= 0:
            raise ValueError("workflow limit must be a positive integer")
        raw = context.state.get(namespace, {})
        if not isinstance(raw, dict):
            raise ValueError("workflow registry must be a JSON object")
        for key, value in raw.items():
            _require_name(key, "job key")
            if not isinstance(value, dict):
                raise ValueError("workflow job must be a JSON object")
        self.context = context
        self.namespace = namespace
        self.limit = limit
        # Handles for the same namespace must share membership as well as values.
        # Copying this dictionary lets a later commit erase another handle's jobs.
        self.jobs: dict[str, MutableMapping[str, Any]] = raw
        context.state[namespace] = raw

    def get(self, key: str) -> Job | None:
        value = self.jobs.get(key)
        return Job(key, value) if value is not None else None

    def require(self, key: str, expected: str | Iterable[str] | None = None) -> Job:
        job = self.get(key)
        if job is None:
            raise KeyError(f"job {key} does not exist")
        return job.require(expected) if expected is not None else job

    def start(self, key: str, state: str, now: datetime, **values: Any) -> tuple[Job, bool]:
        _require_name(key, "job key")
        existing = self.get(key)
        if existing is not None:
            return existing, False
        if len(self.jobs) >= self.limit:
            raise WorkflowCapacityError(
                f"workflow capacity of {self.limit} jobs reached; increase the limit "
                "or explicitly archive records before starting another job"
            )
        value: MutableMapping[str, Any] = {**values, "state": state, "createdAt": now.isoformat(), "updatedAt": now.isoformat()}
        self.jobs[key] = value
        return Job(key, value), True

    def defer(self, job: Job, step: str, until: datetime, data: dict[str, Any] | None = None) -> None:
        if self.jobs.get(job.key) is not job.value:
            raise ValueError("job does not belong to this workflow")
        _require_name(step, "continuation step")
        if data is not None and ("jobId" in data or "step" in data):
            raise ValueError("continuation data must not override jobId or step")
        payload = {"jobId": job.key, "step": step, **(data or {})}
        self.context.defer(key=f"{job.key}.{step}", until=until, data=payload)

    def commit(self) -> None:
        """Check the shared registry; ``Context.commit`` persists it with the run."""
        if self.context.state.get(self.namespace) is not self.jobs:
            raise RuntimeError("workflow namespace was replaced while its handle was in use")
