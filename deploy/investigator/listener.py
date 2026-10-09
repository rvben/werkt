#!/usr/bin/env python3
"""Wake on committed classifier results; reconcile GitHub only every six hours."""

import argparse
import fcntl
import json
import os
from pathlib import Path
import select
import time

import investigation as adapter
from worker import Cloud, GitHub, Worker, atomic_json, load_config, now

RECONCILE_SECONDS = 6 * 60 * 60
CLOUD_OBSERVE_SECONDS = 60


class Outbox:
    def __init__(self, connection):
        self.connection = connection
        connection.autocommit = True
        # Subscribe before reading pending work: notifications during the read
        # stay buffered and are drained by wait(), preventing a lost wakeup.
        with connection.cursor() as cursor:
            cursor.execute("LISTEN werkt_investigation_dispatch")

    def pending(self):
        with self.connection.cursor() as cursor:
            cursor.execute("SELECT repository, issue_number, generation FROM investigation_dispatch.requests WHERE pending ORDER BY updated_at, repository, issue_number LIMIT 200")
            return [{"repository": repo, "issueNumber": number, "generation": generation}
                    for repo, number, generation in cursor.fetchall()]

    def acknowledge(self, requests, result):
        handled = {(item["repository"], item["issue"]) for item in result.get("skipped", [])}
        if result.get("acceptedSubject"):
            item = result["acceptedSubject"]
            handled.add((item["repository"], item["issue"]))
        with self.connection.cursor() as cursor:
            for request in requests:
                if (request["repository"], request["issueNumber"]) in handled:
                    # A later event for the same issue must not be swallowed by
                    # an acknowledgement of an earlier queue snapshot.
                    cursor.execute("UPDATE investigation_dispatch.requests SET pending=false WHERE repository=%s AND issue_number=%s AND generation=%s",
                                   (request["repository"], request["issueNumber"], request["generation"]))

    def wait(self, seconds):
        if not self.connection.notifies:
            select.select([self.connection], [], [], seconds)
        self.connection.poll()
        self.connection.notifies.clear()


def cycle(worker, outbox, reconcile=False):
    pending = outbox.pending()
    result = worker.run(requests=pending)
    outbox.acknowledge(pending, result)
    # A fallback scan must not bypass queued or currently owned work.
    if reconcile and result["status"] == "idle":
        result = worker.run()
        result["reconciled"] = True
        result["trigger"] = "reconciliation"
    else:
        result["trigger"] = "classification_event" if pending else "registry_or_cloud_observation"
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True)
    parser.add_argument("--state-dir", required=True)
    args = parser.parse_args()
    directory = Path(args.state_dir)
    os.umask(0o077)
    with (directory / "worker.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            import psycopg2
            connection = psycopg2.connect(dbname="werkt", host="/var/run/postgresql", connect_timeout=10)
            outbox = Outbox(connection)
            config = load_config(args.config)
            registry = adapter.Registry(os.environ.get("WERKT_URL", "http://127.0.0.1:8080"), os.environ.get("WERKT_INVESTIGATION_OPERATE_TOKEN"))
            worker = Worker(config, registry, GitHub(), Cloud(config["codexCli"]), directory)
            saved = directory / "last-reconcile.json"
            last_reconcile = json.loads(saved.read_text())["at"] if saved.exists() else 0
            while True:
                due = time.time() - last_reconcile >= RECONCILE_SECONDS
                result = cycle(worker, outbox, reconcile=due)
                if result.get("reconciled"):
                    last_reconcile = time.time()
                    atomic_json(saved, {"at": last_reconcile})
                result.update(checkedAt=now(), nextReconciliationAt=last_reconcile + RECONCILE_SECONDS)
                atomic_json(directory / "status.json", result)
                print(json.dumps(result), flush=True)
                # No idle GitHub polling. Only in-flight cloud work needs a
                # periodic observation because this CLI has no completion hook.
                running = result.get("workflowStatus") == "running" or result["status"] == "submitted"
                watching = bool(worker.review_queue())
                delay = CLOUD_OBSERVE_SECONDS if running or watching else max(60, last_reconcile + RECONCILE_SECONDS - time.time())
                if result["status"] in {"occupied", "submission_unknown"} and not running and not watching:
                    delay = RECONCILE_SECONDS
                if result["status"] == "idle" and outbox.pending():
                    delay = 0  # Drain a bounded batch of obsolete events without a six-hour delay.
                outbox.wait(min(delay, RECONCILE_SECONDS))
        except Exception as error:
            reason = str(error) if isinstance(error, adapter.Unavailable) else "Listener dependency or persistent state needs review."
            atomic_json(directory / "status.json", {"status": "blocked", "checkedAt": now(), "reason": reason})
            print(json.dumps({"status": "blocked", "reason": reason}), flush=True)
            raise SystemExit(1) from None


if __name__ == "__main__":
    main()
