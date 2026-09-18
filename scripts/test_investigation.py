import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

import investigation as inv


SPEC = {"requestId": "request-one", "repositoryId": "123", "repository": "example/project", "issueNumber": 42,
        "issueFingerprint": "a" * 64, "expectedBaseSha": "b" * 40, "environmentId": "env", "branch": "main"}
TASK = {"id": "task_example", "status": "ready", "updated_at": "2026-01-01T00:00:00Z", "attempt_total": 1}


class FakeRegistry:
    def __init__(self):
        self.record = None
        self.events = []
        self.fail_after_launch = False

    def lookup(self, *args):
        return [copy.deepcopy(self.record)] if self.record else []

    def call(self, method, path, data=None):
        if method == "GET":
            return copy.deepcopy(self.record)
        self.record = {**data, "version": 1, "state": {"status": "reserved"}}
        self.events.append("reserved")
        return {"created": True, "investigation": copy.deepcopy(self.record)}

    def update(self, record, state):
        if self.fail_after_launch and state["status"] == "running":
            raise inv.Unavailable("lost connection after cloud accepted")
        self.events.append(state["status"])
        self.record = {**record, "version": record["version"] + 1, "state": copy.deepcopy(state)}
        return copy.deepcopy(self.record)


class FakeCloud:
    cli = "codex"

    def __init__(self, registry, fail=False):
        self.registry, self.fail = registry, fail
        self.calls = 0
        self.change_during_capture = False

    def submit(self, spec, prompt):
        self.calls += 1
        assert self.registry.record["state"]["status"] == "submission_unknown"
        if self.fail:
            raise inv.Unavailable("timeout")
        return "task_example", "https://chatgpt.com/codex/cloud/tasks/task_example"

    def tasks(self, ids):
        task = dict(TASK)
        if self.change_during_capture and self.calls:
            task["updated_at"] = "2026-01-02T00:00:00Z"
        return {"task_example": task}

    def run(self, *args, **kwargs):
        self.calls += 1
        return "diff --git a/example b/example\n"


class InvestigationTests(unittest.TestCase):
    def test_durable_intent_precedes_cloud_and_replay_does_not_submit(self):
        r = FakeRegistry(); c = FakeCloud(r)
        self.assertEqual(inv.launch(r, SPEC, "prompt", c)["status"], "submitted")
        self.assertEqual(r.events, ["reserved", "submission_unknown", "running"])
        self.assertEqual(inv.launch(r, SPEC, "prompt", c)["status"], "existing_investigation")
        self.assertEqual(c.calls, 1)

    def test_timeout_never_retries_even_with_new_generation(self):
        r = FakeRegistry(); c = FakeCloud(r, fail=True)
        self.assertEqual(inv.launch(r, SPEC, "prompt", c)["status"], "submission_unknown")
        self.assertEqual(inv.launch(r, SPEC, "prompt", c, True)["status"], "existing_investigation")
        self.assertEqual(c.calls, 1)

    def test_lost_response_after_acceptance_preserves_task_for_reconciliation(self):
        r = FakeRegistry(); r.fail_after_launch = True; c = FakeCloud(r)
        result = inv.launch(r, SPEC, "prompt", c)
        self.assertEqual(result["status"], "submission_unknown")
        self.assertEqual(result["taskId"], "task_example")
        self.assertEqual(r.record["state"]["status"], "submission_unknown")

    def test_registry_unavailable_is_never_none(self):
        r = inv.Registry("https://example.com", "fixture")
        r.call = lambda *args: {"status": "none", "items": [{}], "nextCursor": ""}
        with self.assertRaises(inv.Unavailable):
            r.lookup("123", 42)

    def test_registry_pagination_must_be_complete_and_subject_matches(self):
        r = inv.Registry("https://example.com", "fixture")
        for page in ({"status":"none","items":[],"nextCursor":"again"},
                     {"status":"found","items":[{**SPEC,"repositoryId":"456","version":1,"state":{}}],"nextCursor":""}):
            r.call = lambda *args: page
            with self.assertRaises(inv.Unavailable):
                r.lookup("123",42)

    def test_cloud_missing_malformed_expired_and_version_drift(self):
        for response in ('{"tasks":[],"cursor":null}', '{"tasks":[{}]}', '{"tasks":[],"cursor":"same"}', 'not JSON'):
            def run(args, **kwargs):
                return inv.CLI_VERSION if args[-1] == "--version" else response
            with self.assertRaises(inv.Unavailable):
                inv.Cloud("codex", run).tasks({"task_example"})
        with self.assertRaises(inv.Unavailable):
            inv.Cloud("codex", lambda *args: "different version")
        with self.assertRaises(inv.Unavailable):
            inv.Cloud("codex", lambda *args: (_ for _ in ()).throw(inv.Unavailable("expired login")))

    def test_stale_issue_base_and_cloud_are_visible(self):
        r = FakeRegistry(); c = FakeCloud(r); inv.launch(r, SPEC, "prompt", c)
        r.record["state"].update(status="local",cloudUpdatedAt="2025-12-31T00:00:00Z",cloudStatus="ready")
        result = inv.lookup(r, {**SPEC,"issueFingerprint":"c"*64,"expectedBaseSha":"d"*40}, lambda:c)
        freshness = result["records"][0]["freshness"]
        self.assertTrue(all(freshness.values()), freshness)

    def test_cloud_failure_keeps_saved_handoff_but_blocks_reuse(self):
        r = FakeRegistry(); c = FakeCloud(r); inv.launch(r, SPEC, "prompt", c)
        r.record["state"]["handoff"] = "Saved findings"
        def unavailable(): raise inv.Unavailable("expired login")
        result = inv.lookup(r, SPEC, unavailable)
        self.assertEqual(result["status"], "lookup_unavailable")
        self.assertEqual(result["records"][0]["state"]["handoff"], "Saved findings")

    def test_capture_brackets_download_and_never_overwrites(self):
        r = FakeRegistry(); c = FakeCloud(r); inv.launch(r, SPEC, "prompt", c); c.calls = 0
        with tempfile.TemporaryDirectory() as temp:
            result = inv.capture(r, SPEC["requestId"], c, Path(temp), "Tests pass", "b"*40, 1)
            self.assertEqual(result["status"], "ready_for_review")
            patch = Path(result["record"]["state"]["patchRef"])
            self.assertEqual(hashlib.sha256(patch.read_bytes()).hexdigest(),result["record"]["state"]["patchSha256"])
            inv.capture(r, SPEC["requestId"], c, Path(temp), "Tests pass", "b"*40, 1)
            self.assertEqual(len(list(Path(temp).glob("*.patch"))),2)

    def test_cloud_change_during_capture_leaves_registry_and_files_untouched(self):
        r = FakeRegistry(); c = FakeCloud(r); inv.launch(r, SPEC, "prompt", c); c.calls = 0; c.change_during_capture = True
        original = copy.deepcopy(r.record)
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaises(inv.Unavailable):
                inv.capture(r, SPEC["requestId"], c, Path(temp), "Tests pass", "b"*40, 1)
            self.assertEqual(list(Path(temp).iterdir()),[])
            self.assertEqual(r.record,original)

    def test_claim_checks_cloud_and_saved_patch_before_ownership(self):
        r = FakeRegistry(); c = FakeCloud(r); inv.launch(r, SPEC, "prompt", c)
        with tempfile.TemporaryDirectory() as temp:
            inv.capture(r, SPEC["requestId"], c, Path(temp), "Verified findings", "b"*40, 1)
            record = copy.deepcopy(r.record)
            c.change_during_capture = True
            with self.assertRaises(inv.Unavailable):
                inv.claim(r, SPEC["requestId"], c)
            self.assertEqual(r.record,record)
            c.change_during_capture = False
            patch = Path(record["state"]["patchRef"])
            original = patch.read_bytes(); patch.write_bytes(b"tampered")
            with self.assertRaises(inv.Unavailable):
                inv.claim(r, SPEC["requestId"], c)
            self.assertEqual(r.record,record)
            patch.write_bytes(original)
            self.assertEqual(inv.claim(r, SPEC["requestId"], c)["status"],"local")

    def test_insecure_registry_urls_rejected(self):
        for url in ("http://example.com", "https://secret@example.com", "https://example.com?token=secret", "file:///tmp/socket"):
            with self.assertRaises(inv.Unavailable):
                inv.Registry(url,"fixture")
        inv.Registry("http://127.0.0.1:8080","fixture")


if __name__ == "__main__":
    unittest.main()
