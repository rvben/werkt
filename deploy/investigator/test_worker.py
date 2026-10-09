import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlparse

import investigation as adapter
import worker

CONFIG = {"enabled": True, "codexCli": "/bin/codex", "repositories": {
    "example/project": {"repositoryId": "123", "environmentId": "env-1", "branch": "main"}}}


def issue(number=42, labels=("urgency:high",), state="open"):
    return {"number": number, "state": state, "title": "Crashes on startup", "body": "A concrete reproducer",
            "labels": [{"name": label} for label in labels]}


class Registry:
    def __init__(self):
        self.records = {}
        self.events = []
        self.lose_response = False

    def lookup(self, repo, number):
        return [copy.deepcopy(r) for r in self.records.values() if r["repositoryId"] == repo and r["issueNumber"] == number]

    def call(self, method, path, data=None):
        if method == "GET":
            if path[1:] not in self.records:
                raise adapter.Unavailable("missing reservation after interruption")
            return copy.deepcopy(self.records[path[1:]])
        assert method == "POST"
        if data["requestId"] in self.records:
            return {"created": False, "investigation": copy.deepcopy(self.records[data["requestId"]])}
        self.events.append("reserved")
        r = dict(data, version=1, state={"status": "reserved"})
        self.records[r["requestId"]] = r
        return {"created": True, "investigation": copy.deepcopy(r)}

    def update(self, record, state):
        assert self.records[record["requestId"]]["version"] == record["version"]
        self.events.append(state["status"])
        value = dict(record, version=record["version"] + 1, state=copy.deepcopy(state))
        self.records[record["requestId"]] = value
        if self.lose_response and state["status"] == "running":
            raise adapter.Unavailable("response lost")
        return copy.deepcopy(value)


class GitHub:
    def __init__(self, issues):
        self.issues = issues
        self.change = None

    def labeled(self, repo, label):
        return [copy.deepcopy(i) for i in self.issues if {"name": label} in i["labels"]]

    def subject(self, repo, repo_id, number, branch):
        return {"repository": repo, "repositoryId": repo_id, "issueNumber": number, "branch": branch,
                "issueFingerprint": "a" * 64, "expectedBaseSha": "b" * 40}, copy.deepcopy(self.change or next(i for i in self.issues if i["number"] == number))


class Cloud:
    def __init__(self, registry, directory):
        self.registry, self.directory = registry, directory
        self.submissions = []
        self.fail = False
        self.auth_fail = False
        self.status = "running"

    def tasks(self, ids):
        if self.auth_fail:
            raise adapter.Unavailable("expired login")
        return {key: {"id": key, "status": self.status, "updated_at": "2026-09-18T12:00:00Z", "attempt_total": 1} for key in ids}

    def submit(self, spec, prompt):
        # This is the central crash/replay invariant, not just an output check.
        assert self.registry.records[spec["requestId"]]["state"]["status"] == "submission_unknown"
        assert json.loads((self.directory / "dispatch.json").read_text())["active"] == spec
        self.submissions.append((spec, prompt))
        if self.fail:
            raise adapter.Unavailable("ambiguous timeout")
        return "task_example", "https://chatgpt.com/codex/cloud/tasks/task_example"


class WorkerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.directory = Path(self.tmp.name)
        worker.atomic_json(self.directory / "dispatch.json", {"schema": 1, "active": None})
        self.registry = Registry()
        self.github = GitHub([issue()])
        self.cloud = Cloud(self.registry, self.directory)
        self.worker = worker.Worker(CONFIG, self.registry, self.github, self.cloud, self.directory)

    def test_high_submits_once_with_durable_intent_and_no_publication(self):
        self.assertEqual(self.worker.run()["status"], "submitted")
        self.assertEqual(self.registry.events, ["reserved", "submission_unknown", "running"])
        self.assertEqual(self.worker.run()["status"], "occupied")
        self.assertEqual(len(self.cloud.submissions), 1)
        self.assertIn("Do not push", self.cloud.submissions[0][1])

    def test_normal_low_critical_conflicting_labels_and_closed_never_launch(self):
        for value in (issue(labels=("urgency:normal",)), issue(labels=("urgency:low",)),
                      issue(labels=("urgency:critical-review",)), issue(state="closed"),
                      issue(labels=("urgency:high", "urgency:critical-review")),
                      issue(labels=("urgency:high", "urgency:normal")), dict(issue(), pull_request={})):
            with self.subTest(value=value):
                self.github.issues = [value]
                self.assertEqual(self.worker.run()["status"], "idle")
        self.assertFalse(self.cloud.submissions)

    def test_critical_is_visible_for_review(self):
        self.github.issues = [issue(labels=("urgency:critical-review",))]
        self.assertEqual(self.worker.run()["criticalReview"], [{"repository": "example/project", "issue": 42}])

    def test_rechecks_label_and_open_state_before_submission(self):
        for changed in (issue(labels=()), issue(state="closed")):
            self.github.change = changed
            self.assertEqual(self.worker.run()["status"], "idle")
        self.assertFalse(self.cloud.submissions)

    def test_dry_run_has_no_mutations(self):
        before = (self.directory / "dispatch.json").read_bytes()
        self.assertEqual(self.worker.run(True)["status"], "would_submit")
        self.assertEqual((self.directory / "dispatch.json").read_bytes(), before)
        self.assertFalse(self.registry.events)
        self.assertFalse(self.cloud.submissions)

    def test_ambiguous_submission_stops_all_following_issues(self):
        self.cloud.fail = True
        self.assertEqual(self.worker.run()["status"], "submission_unknown")
        self.github.issues.append(issue(43))
        for _ in range(3):
            self.assertEqual(self.worker.run()["status"], "occupied")
        self.assertEqual(len(self.cloud.submissions), 1)

    def test_lost_running_response_recovers_without_resubmission(self):
        self.registry.lose_response = True
        self.assertEqual(self.worker.run()["status"], "submission_unknown")
        self.registry.lose_response = False
        self.assertEqual(self.worker.run()["status"], "occupied")
        self.assertEqual(len(self.cloud.submissions), 1)

    def test_finished_cloud_requests_manual_capture_and_starts_next_issue(self):
        self.worker.run()
        self.cloud.status = "ready"
        self.cloud.run = lambda *a, **kw: self.fail("observer must not export a patch")
        self.registry.api = lambda *a, **kw: self.fail("observer must not call factory APIs")
        self.github.issues.append(issue(43))
        self.assertEqual(self.worker.run()["status"], "submitted")
        self.assertIn("auto-high-123-42-v1", self.worker.review_queue())
        state = next(iter(self.registry.records.values()))["state"]
        self.assertEqual(state["status"], "needs_input")
        self.assertIn("explicitly capture", state["reason"])
        self.assertNotIn("actualBaseSha", state)
        self.assertNotIn("patchSha256", state)
        self.assertEqual(len(self.cloud.submissions), 2)

    def test_ready_conversation_remains_observed_without_factory_handoff(self):
        self.worker.run()
        self.cloud.status = "ready"
        self.assertEqual(self.worker.run()["status"], "idle")
        self.cloud.status = "waiting_for_input"
        self.assertEqual(self.worker.run()["status"], "idle")
        state = next(iter(self.registry.records.values()))["state"]
        self.assertEqual(state["status"], "needs_input")
        self.assertIn("needs clarification", state["reason"])
        self.assertEqual(len(self.cloud.submissions), 1)

    def test_unchanged_observation_does_not_race_explicit_capture(self):
        self.worker.run()
        self.cloud.status = "ready"
        self.worker.run()
        before = copy.deepcopy(next(iter(self.registry.records.values())))
        self.worker.run()
        self.assertEqual(next(iter(self.registry.records.values())), before)

    def test_local_takeover_releases_active_slot_without_factory_queue(self):
        self.worker.run()
        record = next(iter(self.registry.records.values()))
        record["state"]["status"] = "local"
        self.github.issues.append(issue(43))
        self.assertEqual(self.worker.run()["status"], "submitted")
        self.assertEqual(len(self.cloud.submissions), 2)
        self.assertNotIn(record["requestId"], self.worker.review_queue())

    def test_operator_capture_wins_changed_observation_without_stopping_dispatch(self):
        self.worker.run()
        record = copy.deepcopy(next(iter(self.registry.records.values())))
        self.cloud.status = "ready"
        def raced_update(old, state):
            fresh = self.registry.records[old["requestId"]]
            fresh["version"] += 1
            fresh["state"].update(status="review", patchSha256="c" * 64)
            raise adapter.Unavailable("registry HTTP 409; refresh before retrying")
        self.registry.update = raced_update
        result = self.worker.observe(record)
        self.assertEqual(result["state"]["status"], "review")
        self.assertEqual(result["state"]["patchSha256"], "c" * 64)

    def test_observation_failure_without_ownership_change_still_blocks(self):
        self.worker.run()
        record = copy.deepcopy(next(iter(self.registry.records.values())))
        self.cloud.status = "ready"
        self.registry.update = lambda *a: (_ for _ in ()).throw(adapter.Unavailable("outage"))
        with self.assertRaises(adapter.Unavailable):
            self.worker.observe(record)

    def test_operator_snapshot_and_local_takeover_are_not_overwritten(self):
        for status in ("review", "local"):
            with self.subTest(status=status):
                self.worker.run()
                record = next(iter(self.registry.records.values()))
                record["state"].update(status=status, actualBaseSha="b" * 40,
                                       patchSha256="c" * 64, cloudUpdatedAt="captured")
                before = copy.deepcopy(record)
                self.cloud.status = "ready"
                self.assertEqual(self.worker.observe(copy.deepcopy(record)), before)
                self.assertEqual(next(iter(self.registry.records.values())), before)

    def test_finished_or_operator_owned_conversations_leave_observation_queue(self):
        self.worker.run()
        self.cloud.status = "ready"
        self.worker.run()
        record = next(iter(self.registry.records.values()))
        for status in ("completed", "abandoned", "review", "local"):
            with self.subTest(status=status):
                record["state"]["status"] = status
                worker.atomic_json(self.directory / "review-queue.json", {record["requestId"]: True})
                self.worker.observe_reviews(dry_run=True)
                self.assertTrue(self.worker.review_queue())
                self.worker.observe_reviews(dry_run=False)
                self.assertEqual(self.worker.review_queue(), {})

    def test_observed_issue_is_never_blindly_resubmitted(self):
        self.worker.run()
        self.cloud.status = "ready"
        self.assertEqual(self.worker.run()["status"], "idle")
        self.assertEqual(self.worker.run()["status"], "idle")
        self.assertEqual(len(self.cloud.submissions), 1)

    def test_terminal_history_is_never_automatically_reopened(self):
        self.worker.run()
        next(iter(self.registry.records.values()))["state"]["status"] = "completed"
        self.assertEqual(self.worker.run()["status"], "idle")
        self.assertEqual(len(self.cloud.submissions), 1)
        self.github.issues.append(issue(43))
        self.assertEqual(self.worker.run()["status"], "submitted")
        self.assertEqual(len(self.cloud.submissions), 2)

    def test_missing_registry_after_durable_intent_does_not_retry(self):
        with patch.object(adapter, "launch", side_effect=adapter.Unavailable("outage")):
            with self.assertRaises(adapter.Unavailable):
                self.worker.run()
        with self.assertRaises(adapter.Unavailable):
            self.worker.run()
        self.assertFalse(self.cloud.submissions)

    def test_expired_login_and_corrupt_journal_fail_before_launch(self):
        self.cloud.auth_fail = True
        with self.assertRaises(adapter.Unavailable):
            self.worker.run()
        self.cloud.auth_fail = False
        (self.directory / "dispatch.json").write_text("broken")
        with self.assertRaises(ValueError):
            self.worker.run()
        self.assertFalse(self.cloud.submissions)

    def test_existing_manual_investigation_does_not_duplicate_dispatch(self):
        self.worker.run()
        worker.atomic_json(self.directory / "dispatch.json", {"schema": 1, "active": None})
        self.assertEqual(self.worker.run()["status"], "idle")
        self.assertEqual(len(self.cloud.submissions), 1)

    def test_disabled_never_checks_or_launches_cloud(self):
        self.worker.config = dict(CONFIG, enabled=False)
        self.cloud.auth_fail = True
        self.assertEqual(self.worker.run()["status"], "disabled")

    def test_github_pagination_is_complete_and_identity_is_pinned(self):
        calls = []
        def fetch(path):
            calls.append(path)
            return [issue(n) for n in range(1, 101)] if parse_qs(urlparse(path).query)["page"] == ["1"] else []
        gh = worker.GitHub(fetch)
        self.assertEqual(len(gh.labeled("example/project", "urgency:high")), 100)
        self.assertEqual(len(calls), 2)
        gh.fetch = lambda path: issue() if "/issues/" in path else {"id": 999, "full_name": "example/project"}
        with self.assertRaises(adapter.Unavailable):
            gh.subject("example/project", "123", 42, "main")

    def test_cli_contract_pinned_and_submit_uses_single_attempt(self):
        calls = []
        def run(argv, **kwargs):
            calls.append(argv)
            return worker.CLI_VERSION if argv[-1] == "--version" else "https://chatgpt.com/codex/tasks/task_example"
        cloud = worker.Cloud("/bin/codex", run)
        self.assertEqual(cloud.submit({"environmentId": "env", "branch": "main"}, "prompt")[0], "task_example")
        self.assertEqual(calls[1], ["/bin/codex", "cloud", "exec", "--env", "env", "--branch", "main", "--attempts", "1", "prompt"])
        with self.assertRaises(adapter.Unavailable):
            worker.Cloud("/bin/codex", lambda *args: "codex-cli newer")


if __name__ == "__main__":
    unittest.main()
