from __future__ import annotations

import json
import os
import tempfile
import unittest
from pathlib import Path

from datetime import UTC, datetime

from werkt import Context, DurableWorkflow, Event, StaleContinuation, execute


class SDKTest(unittest.TestCase):
    def test_execute_adapts_files_to_typed_handler(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            event_path = Path(directory) / "event.json"
            result_path = Path(directory) / "result.json"
            control_path = Path(directory) / "control.json"
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
                        "WERKT_CONTROL_PATH": str(control_path),
                    }
                )

                def handler(event: Event, context: Context) -> dict[str, str]:
                    return {"name": event.data["name"], "runId": context.run_id}

                result = execute(handler)
            finally:
                os.environ.clear()
                os.environ.update(previous)

            self.assertEqual(
                json.loads(result_path.read_text()),
                {"name": "Ada", "runId": "run_test"},
            )
            self.assertEqual(result, {"name": "Ada", "runId": "run_test"})

    def test_context_writes_typed_approval_control(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            control_path = Path(directory) / "control.json"
            context = Context("publish", "rev_test", "run_test", control_path)
            context.request_approval(
                key="publish-42",
                title="Publish recording?",
                expires_at="2026-08-31T12:00:00Z",
                fields=[{"id": "title", "label": "Title", "type": "text", "required": True}],
                actions=[{"id": "approve", "label": "Publish", "requiresFields": True}],
            )
            self.assertEqual(json.loads(control_path.read_text())["approval"]["key"], "publish-42")

    def test_context_accumulates_approval_and_expiry_continuation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            control_path = Path(directory) / "control.json"
            context = Context("publish", "rev_test", "run_test", control_path)
            context.request_approval(
                key="publish-42", title="Publish?", expires_at="2026-09-06T12:00:00Z",
                fields=[], actions=[{"id": "approve", "label": "Publish"}],
            )
            context.defer(key="publish-42.expiry", until="2026-09-06T12:00:00Z", data={"step": "expiry"})
            value = json.loads(control_path.read_text())
            self.assertEqual(value["approval"]["key"], "publish-42")
            self.assertEqual(value["defer"]["data"]["step"], "expiry")

    def test_context_accumulates_provider_neutral_notifications(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            control_path = Path(directory) / "control.json"
            context = Context("recording", "rev_test", "run_test", control_path)
            context.notify(
                key="recording.started",
                title="Recording started",
                body="The Sunday service recording has started.",
            )
            context.notify(
                key="recording.stopped",
                title="Recording stopped",
                body="Media processing can begin.",
                priority="high",
            )
            value = json.loads(control_path.read_text())
            self.assertEqual(value["notifications"][0]["priority"], "default")
            self.assertEqual(value["notifications"][1]["key"], "recording.stopped")

    def test_durable_workflow_deduplicates_and_rejects_stale_steps(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            context = Context("example", "rev", "run", Path(directory) / "control.json", state={})
            workflow = DurableWorkflow(context, limit=2)
            now = datetime(2026, 8, 30, 12, tzinfo=UTC)
            job, created = workflow.start("recording-1", "polling", now, attempts=0)
            duplicate, created_again = workflow.start("recording-1", "polling", now)
            self.assertTrue(created)
            self.assertFalse(created_again)
            self.assertEqual(job.value, duplicate.value)
            job.require("polling").transition("ready", now)
            workflow.commit()
            with self.assertRaises(StaleContinuation):
                job.require("polling")


if __name__ == "__main__":
    unittest.main()
