from __future__ import annotations

import copy
import json
import tempfile
import unittest
from datetime import UTC, datetime, timedelta
from pathlib import Path

from werkt import Context, DurableWorkflow, WorkflowCapacityError


class WorkflowTest(unittest.TestCase):
    def setUp(self) -> None:
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        root = Path(self.directory.name)
        self.context = Context(
            "example", "rev", "run", root / "control.json",
            state_path=root / "state.json", result_path=root / "result.json",
        )
        self.now = datetime(2026, 9, 18, tzinfo=UTC)

    def reload(self) -> Context:
        self.context.commit({"outcome": "saved"})
        return Context(
            "example", "rev", "next-run", self.context.control_path,
            state=json.loads(self.context.state_path.read_text()),
        )

    def test_capacity_preserves_active_and_completed_jobs_and_deduplication(self) -> None:
        workflow = DurableWorkflow(self.context, limit=2)
        workflow.start("active", "polling", self.now)
        workflow.start("finished", "complete", self.now + timedelta(seconds=1))
        before = copy.deepcopy(self.context.state)
        with self.assertRaisesRegex(WorkflowCapacityError, "capacity"):
            workflow.start("new", "queued", self.now + timedelta(seconds=2))
        self.assertEqual(self.context.state, before)
        self.assertIsNone(workflow.get("new"))

        restored = DurableWorkflow(self.reload(), limit=2)
        self.assertEqual(restored.require("active").state, "polling")
        duplicate, created = restored.start("finished", "queued", self.now)
        self.assertFalse(created)
        self.assertEqual(duplicate.state, "complete")

    def test_lowered_capacity_allows_existing_jobs_to_progress(self) -> None:
        workflow = DurableWorkflow(self.context, limit=3)
        for key in ("one", "two", "three"):
            workflow.start(key, "polling", self.now)
        restored = DurableWorkflow(self.reload(), limit=1)
        restored.require("one").transition("complete", self.now)
        restored.commit()
        self.assertEqual(len(restored.jobs), 3)
        self.assertFalse(restored.start("two", "polling", self.now)[1])
        with self.assertRaises(WorkflowCapacityError):
            restored.start("four", "queued", self.now)

    def test_increased_capacity_admits_new_work_without_replacing_old_jobs(self) -> None:
        workflow = DurableWorkflow(self.context, limit=1)
        workflow.start("old", "complete", self.now)
        restored = DurableWorkflow(self.reload(), limit=2)
        _, created = restored.start("new", "queued", self.now)
        self.assertTrue(created)
        self.assertEqual(restored.require("old").state, "complete")

    def test_separate_namespaces_do_not_share_jobs(self) -> None:
        first = DurableWorkflow(self.context, namespace="first")
        second = DurableWorkflow(self.context, namespace="second")
        first.start("same", "polling", self.now)
        second.start("same", "complete", self.now)
        self.assertEqual(first.require("same").state, "polling")
        self.assertEqual(second.require("same").state, "complete")

    def test_commit_rejects_a_replaced_namespace_instead_of_restoring_old_data(self) -> None:
        workflow = DurableWorkflow(self.context)
        workflow.start("old", "complete", self.now)
        self.context.state["jobs"] = {"replacement": {"state": "queued"}}
        with self.assertRaisesRegex(RuntimeError, "replaced"):
            workflow.commit()
        self.assertEqual(set(self.context.state["jobs"]), {"replacement"})

    def test_invalid_capacity_and_namespace_do_not_change_state(self) -> None:
        for limit in (0, -1, True, False, 1.5, "2", None):
            with self.subTest(limit=limit), self.assertRaises(ValueError):
                DurableWorkflow(self.context, limit=limit)
        for namespace in ("", " ", None, 3):
            with self.subTest(namespace=namespace), self.assertRaises(ValueError):
                DurableWorkflow(self.context, namespace=namespace)
        self.assertEqual(self.context.state, {})

    def test_corrupt_registry_is_rejected_without_silently_dropping_records(self) -> None:
        for registry in (None, [], "bad", {"bad": []}, {"bad": None}, {1: {}}, {"": {}}):
            with self.subTest(registry=registry):
                self.context.state = {"jobs": copy.deepcopy(registry), "unrelated": {"keep": True}}
                before = copy.deepcopy(self.context.state)
                with self.assertRaises(ValueError):
                    DurableWorkflow(self.context)
                self.assertEqual(self.context.state, before)

    def test_handles_for_the_same_namespace_share_jobs_and_transitions(self) -> None:
        first = DurableWorkflow(self.context)
        second = DurableWorkflow(self.context)
        job, _ = first.start("one", "polling", self.now)
        second.start("two", "polling", self.now)
        first.commit()
        second.require("one").transition("complete", self.now)
        self.assertEqual(job.state, "complete")
        restored = DurableWorkflow(self.reload())
        self.assertEqual(set(restored.jobs), {"one", "two"})
        self.assertEqual(restored.require("one").state, "complete")

    def test_invalid_new_job_is_rejected_before_mutation(self) -> None:
        workflow = DurableWorkflow(self.context)
        for key in ("", " ", 3, None):
            with self.subTest(key=key), self.assertRaises(ValueError):
                workflow.start(key, "polling", self.now)
        self.assertEqual(workflow.jobs, {})

    def test_defer_cannot_override_routing_or_use_another_workflows_job(self) -> None:
        workflow = DurableWorkflow(self.context)
        job, _ = workflow.start("one", "polling", self.now)
        for data in ({"jobId": "other"}, {"step": "publish"}):
            with self.subTest(data=data), self.assertRaises(ValueError):
                workflow.defer(job, "poll", self.now + timedelta(minutes=1), data)
        self.assertIsNone(self.context.control.defer)

        other = DurableWorkflow(self.context, namespace="other")
        foreign, _ = other.start("one", "polling", self.now)
        with self.assertRaises(ValueError):
            workflow.defer(foreign, "poll", self.now + timedelta(minutes=1))
        self.assertIsNone(self.context.control.defer)

        workflow.defer(job, "poll", self.now + timedelta(minutes=1), {"attempt": 1})
        self.assertEqual(self.context.control.defer["data"], {"jobId": "one", "step": "poll", "attempt": 1})


if __name__ == "__main__":
    unittest.main()
