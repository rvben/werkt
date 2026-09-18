from __future__ import annotations

import copy
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from werkt import Context


class ControlTest(unittest.TestCase):
    def setUp(self) -> None:
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        root = Path(self.directory.name)
        self.context = Context("example", "rev", "run", root / "control.json")

    def test_conflicting_timer_cannot_replace_existing_timer(self) -> None:
        timer = dict(key="poll", until="2026-09-18T10:00:00Z", data={"step": "poll"})
        self.context.defer(**timer)
        before = self.context.control_path.read_bytes()
        self.context.defer(**timer)
        for update in ({"key": "other"}, {"until": "2026-09-18T11:00:00Z"}, {"data": {"step": "publish"}}):
            with self.subTest(update=update), self.assertRaisesRegex(ValueError, "continuation"):
                self.context.defer(**(timer | update))
            self.assertEqual(self.context.control_path.read_bytes(), before)
        self.context.commit({})
        self.assertEqual(self.context.control_path.read_bytes(), before)

    def test_conflicting_approval_cannot_replace_existing_approval(self) -> None:
        approval = dict(key="publish", title="Publish?", expires_at="2026-09-18T10:00:00Z", fields=[], actions=[])
        self.context.request_approval(**approval)
        before = self.context.control_path.read_bytes()
        self.context.request_approval(**approval)
        for update in ({"key": "other"}, {"title": "Changed decision?"}):
            with self.subTest(update=update), self.assertRaisesRegex(ValueError, "approval"):
                self.context.request_approval(**(approval | update))
            self.assertEqual(self.context.control_path.read_bytes(), before)
        self.context.commit({})
        self.assertEqual(self.context.control_path.read_bytes(), before)

    def test_queued_controls_snapshot_mutable_input(self) -> None:
        data = {"ids": [1]}
        fields = [{"id": "name", "type": "text"}]
        actions = [{"id": "approve", "label": "Publish"}]
        self.context.defer(key="poll", until="2026-09-18T10:00:00Z", data=data)
        self.context.request_approval(key="publish", title="Publish?", expires_at="2026-09-18T10:00:00Z", fields=fields, actions=actions)
        before = self.context.control_path.read_bytes()
        data["ids"].append(2)
        fields[0]["id"] = "different"
        actions.clear()
        self.context.commit({})
        self.assertEqual(self.context.control_path.read_bytes(), before)

    def test_invalid_control_does_not_poison_previously_queued_work(self) -> None:
        self.context.notify(key="ok", title="Ready", body="Work completed")
        before = copy.deepcopy(self.context.control.as_dict())
        with self.assertRaises((TypeError, ValueError)):
            self.context.defer(key="bad", until="2026-09-18T10:00:00Z", data={"value": object()})
        self.assertEqual(self.context.control.as_dict(), before)
        self.context.commit({})
        self.assertEqual(json.loads(self.context.control_path.read_text()), before)

    def test_failed_control_write_does_not_replace_queued_work(self) -> None:
        self.context.notify(key="ok", title="Ready", body="Done")
        before = copy.deepcopy(self.context.control.as_dict())
        with patch.object(Path, "write_text", side_effect=OSError("disk full")):
            with self.assertRaises(OSError):
                self.context.defer(key="poll", until="2026-09-18T10:00:00Z")
        self.context.commit({})
        self.assertEqual(json.loads(self.context.control_path.read_text()), before)

    def test_nonfinite_control_values_are_rejected_before_queuing(self) -> None:
        for value in (float("nan"), float("inf"), float("-inf")):
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.context.defer(key="poll", until="2026-09-18T10:00:00Z", data={"value": value})
            self.assertIsNone(self.context.control.defer)

    def test_notifications_reject_duplicate_keys_and_capacity_before_mutating(self) -> None:
        self.context.notify(key="one", title="Ready", body="Done")
        with self.assertRaisesRegex(ValueError, "key"):
            self.context.notify(key="one", title="Different", body="Done")
        for i in range(7):
            self.context.notify(key=f"extra-{i}", title="Ready", body="Done")
        before = self.context.control_path.read_bytes()
        with self.assertRaisesRegex(ValueError, "eight"):
            self.context.notify(key="overflow", title="Ready", body="Done")
        self.context.commit({})
        self.assertEqual(self.context.control_path.read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
