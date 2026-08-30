from __future__ import annotations

from dataclasses import dataclass
from datetime import UTC, date, datetime
from zoneinfo import ZoneInfo


@dataclass(frozen=True)
class CalendarEvent:
    summary: str
    starts_at: datetime | date | None
    properties: dict[str, str]


class ICalendar:
    @staticmethod
    def parse(value: str, default_timezone: ZoneInfo) -> list[CalendarEvent]:
        lines: list[str] = []
        for raw in value.replace("\r\n", "\n").replace("\r", "\n").split("\n"):
            if raw.startswith((" ", "\t")) and lines:
                lines[-1] += raw[1:]
            else:
                lines.append(raw)
        events: list[CalendarEvent] = []
        current: dict[str, str] | None = None
        for line in lines:
            if line == "BEGIN:VEVENT":
                current = {}
            elif line == "END:VEVENT" and current is not None:
                events.append(CalendarEvent(current.get("SUMMARY", ""), ICalendar._start(current.get("DTSTART", ""), default_timezone), current))
                current = None
            elif current is not None and ":" in line:
                key = line.split(":", 1)[0].split(";", 1)[0]
                if key in {"SUMMARY", "DTSTART", "UID", "DESCRIPTION"}:
                    current[key] = line
                    if key == "SUMMARY":
                        current[key] = line.split(":", 1)[1].replace("\\,", ",")
        return events

    @staticmethod
    def _start(field: str, timezone: ZoneInfo) -> datetime | date | None:
        _, separator, raw = field.partition(":")
        if not separator:
            return None
        try:
            if len(raw) == 8:
                return datetime.strptime(raw, "%Y%m%d").date()
            if raw.endswith("Z"):
                return datetime.strptime(raw, "%Y%m%dT%H%M%SZ").replace(tzinfo=UTC)
            return datetime.strptime(raw, "%Y%m%dT%H%M%S").replace(tzinfo=timezone)
        except ValueError:
            return None
