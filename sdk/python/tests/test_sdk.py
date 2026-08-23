from __future__ import annotations

import json
import os
import tempfile
import unittest
from pathlib import Path

from werkt import Context, Event, execute


class SDKTest(unittest.TestCase):
    def test_execute_adapts_files_to_typed_handler(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            event_path = Path(directory) / "event.json"
            result_path = Path(directory) / "result.json"
            event_path.write_text(
                json.dumps(
                    {
                        "id": "evt_test",
                        "occurredAt": "2026-08-17T10:00:00Z",
                        "receivedAt": "2026-08-17T10:00:01Z",
                        "trigger": {"type": "webhook"},
                        "data": {"name": "Ada"},
                    }
                )
            )
            previous = os.environ.copy()
            try:
                os.environ.update(
                    {
                        "WERKT_AUTOMATION_ID": "hello",
                        "WERKT_REVISION_ID": "rev_test",
                        "WERKT_RUN_ID": "run_test",
                        "WERKT_EVENT_PATH": str(event_path),
                        "WERKT_RESULT_PATH": str(result_path),
                    }
                )

                def handler(event: Event, context: Context) -> dict[str, str]:
                    return {"name": event.data["name"], "runId": context.run_id}

                execute(handler)
            finally:
                os.environ.clear()
                os.environ.update(previous)

            self.assertEqual(
                json.loads(result_path.read_text()),
                {"name": "Ada", "runId": "run_test"},
            )


if __name__ == "__main__":
    unittest.main()
