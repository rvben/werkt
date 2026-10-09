import unittest
from unittest.mock import Mock, MagicMock

import listener
import test_worker as fixtures
from test_worker import issue


class EventTests(unittest.TestCase):
    setUp = fixtures.WorkerTests.setUp
    def test_event_driven_launch_does_not_scan_github(self):
        self.github.labeled = Mock(side_effect=AssertionError("full scan"))
        outbox = Mock()
        outbox.pending.return_value = [{"repository": "example/project", "issueNumber": 42, "generation": 1}]
        result = listener.cycle(self.worker, outbox)
        self.assertEqual(result["status"], "submitted")
        outbox.acknowledge.assert_called_once()

    def test_empty_queue_does_not_query_github(self):
        self.github.labeled = Mock(side_effect=AssertionError("full scan"))
        outbox = Mock()
        outbox.pending.return_value = []
        self.assertEqual(listener.cycle(self.worker, outbox)["status"], "idle")
        self.assertFalse(self.cloud.submissions)

    def test_reconciliation_recovers_missed_event(self):
        outbox = Mock()
        outbox.pending.return_value = []
        self.assertEqual(listener.cycle(self.worker, outbox, reconcile=True)["status"], "submitted")

    def test_event_rechecks_removed_label(self):
        self.github.change = issue(labels=[])
        result = self.worker.run(requests=[{"repository": "example/project", "issueNumber": 42}])
        self.assertEqual(result["status"], "idle")
        self.assertEqual(result["skipped"][0]["reason"], "eligibility changed")
        self.assertFalse(self.cloud.submissions)

    def test_new_pending_issue_can_start_while_review_waits(self):
        self.worker.run()
        self.cloud.status = "ready"
        self.github.issues.append(issue(43))
        result = self.worker.run(requests=[{"repository": "example/project", "issueNumber": 43}])
        self.assertEqual(result["status"], "submitted")
        self.assertIn("acceptedSubject", result)
        self.assertEqual(len(self.cloud.submissions), 2)

    def test_pending_issue_starts_when_active_cloud_work_is_taken_over_locally(self):
        self.worker.run()
        record = next(iter(self.registry.records.values()))
        record["state"]["status"] = "local"
        self.github.issues.append(issue(43))
        outbox = Mock()
        outbox.pending.return_value = [{"repository": "example/project", "issueNumber": 43, "generation": 1}]
        self.assertEqual(listener.cycle(self.worker, outbox)["status"], "submitted")
        outbox.acknowledge.assert_called_once()
        self.assertEqual(len(self.cloud.submissions), 2)

    def test_acknowledgement_is_generation_pinned(self):
        connection = MagicMock()
        cursor = connection.cursor.return_value.__enter__.return_value = Mock()
        outbox = listener.Outbox(connection)
        outbox.acknowledge([{"repository": "example/project", "issueNumber": 42, "generation": 7}],
                           {"acceptedSubject": {"repository": "example/project", "issue": 42}})
        query, values = cursor.execute.call_args.args
        self.assertIn("generation=%s", query)
        self.assertEqual(values, ("example/project", 42, 7))
