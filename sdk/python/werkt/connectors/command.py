from __future__ import annotations

import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Sequence

from .base import ConnectorError


@dataclass(frozen=True)
class CommandResult:
    returncode: int
    stdout: bytes
    stderr: bytes


class CommandRunner:
    def run(self, command: Sequence[str], *, timeout: float = 120, input: bytes | None = None, output: Path | None = None, label: str = "command") -> CommandResult:
        if not command or not all(isinstance(value, str) and value for value in command):
            raise ConnectorError("command must be a non-empty argument vector")
        try:
            completed = subprocess.run(list(command), input=input, capture_output=True, timeout=timeout, check=False)
        except (OSError, subprocess.TimeoutExpired) as error:
            raise ConnectorError(f"{label} failed") from error
        if completed.returncode != 0 or (output is not None and (not output.exists() or output.stat().st_size == 0)):
            raise ConnectorError(f"{label} failed")
        return CommandResult(completed.returncode, completed.stdout, completed.stderr)
