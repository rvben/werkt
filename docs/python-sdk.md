# Python automation SDK

Python packages are code-first automations. Werkt injects its dependency-free
SDK into every Python artifact and makes it importable during deployment checks,
builds, and runs. A package does not vendor the SDK or install it from PyPI.
The embedded SDK digest participates in the immutable revision hash, so a
platform SDK upgrade cannot silently change an existing revision.

## Minimal handler

```python
from datetime import UTC, datetime, timedelta

from werkt import Context, Event, automation, execute


@automation
def handle(event: Event, context: Context) -> dict:
    if event.trigger_type == "webhook":
        context.state["lastEvent"] = event.id
        context.defer(
            key=f"{event.id}.follow-up",
            until=datetime.now(UTC) + timedelta(minutes=5),
            data={"eventId": event.id, "step": "follow-up"},
        )
        return {"outcome": "scheduled"}
    return {"outcome": "ignored", "trigger": event.trigger_type}


if __name__ == "__main__":
    execute(handle)
```

`execute` adapts Werkt's language-neutral file protocol to typed `Event` and
`Context` values. `Context.commit` writes state, control, and result files; the
runner only persists them after a successful process exit.

## Durable workflows

`DurableWorkflow` is a bounded job registry backed by transactional automation
state. `start` is idempotent, `Job.require` rejects a continuation that no longer
matches the current state, and `Job.transition` records a timestamped state
change. Catch `StaleContinuation` only when an obsolete timer should complete as
a successful no-op.

The registry retains **all** records, including completed jobs: forgetting a
completed key would allow a repeated event to start its work again. The default
`limit=128` caps admission of new jobs. At capacity, `start` raises
`WorkflowCapacityError` before changing state. Looking up, deduplicating, and
advancing existing jobs still work, even when a restored registry exceeds a
lowered limit. Increase the limit or implement an explicit archival policy;
nothing is automatically evicted. Archiving a key also removes its deduplication
protection, so preserve a completion marker elsewhere if events can replay.

```python
from werkt import DurableWorkflow, WorkflowCapacityError

workflow = DurableWorkflow(context, limit=256)
try:
    job, created = workflow.start(event.id, "queued", now)
except WorkflowCapacityError:
    # No existing job was removed. Fail before doing external work so that the
    # event is not acknowledged as processed; resolve capacity before retrying.
    context.log("Workflow capacity reached", limit=workflow.limit)
    raise
```

Workflow handles sharing a context and namespace share the same registry.
Transitions update `context.state` directly; `workflow.commit()` remains
available, while `Context.commit` (called by `execute`) writes the run's state.
Malformed registries are rejected rather than silently reset or filtered.
Namespace and job keys must be non-empty strings, and the limit must be a
positive integer. Do not replace a namespace while holding a workflow handle.

`workflow.defer` accepts only a job belonging to that registry. Its optional
`data` cannot contain the reserved routing fields `jobId` or `step`.

An approval and its expiry continuation can be requested in the same run:

```python
context.request_approval(
    key=f"{job.key}.publish",
    title="Publish result?",
    expires_at=expires_at,
    fields=[{"id": "title", "label": "Title", "type": "text", "required": True}],
    actions=[
        {"id": "approve", "label": "Publish", "style": "primary", "requiresFields": True},
        {"id": "reject", "label": "Skip", "style": "neutral"},
    ],
)
workflow.defer(job, "approval-expiry", expires_at)
```

The run protocol has one continuation slot and one approval slot. Repeating the
same request is a no-op; requesting a different continuation or approval raises
`ValueError` and preserves the original request. Poll multiple jobs with one
batch continuation, or use separate runs. Request arguments are snapshotted so
later changes to a caller's dictionaries or lists do not alter queued work.

Operator notifications stay independent of ntfy, Pushover, or any other
provider configured by the Werkt operator:

```python
context.notify(
    key=f"{job.key}.recording-started",
    title="Recording started",
    body="The Sunday service recording has started.",
)
```

The request is committed only after the run succeeds. Werkt supplies the run
link, routing, provider credentials, retries, and delivery audit.

The SDK rejects duplicate notification keys and more than eight notifications
before altering queued messages. Keys are unique within a run, including for
identical messages. Control requests must be JSON-serializable; failed request
serialization does not leave an invalid request queued in the context. The
runner remains responsible for validating the full control schema and timing.

Use `execution.state.enabled: true` and `execution.concurrency: forbid` for a
durable workflow. External mutations still need idempotency keys or upsert
semantics because state and a remote service cannot share one transaction.

## Connectors

`werkt.connectors` provides small reusable clients for HTTP, OAuth 2 client
credentials, commands, iCalendar, Zoom, OpenAI, Google Sheets, and ntfy. They
have no third-party Python dependencies and raise `ConnectorError` with messages
designed not to disclose request credentials. Google service-account signing
requires an `openssl` executable in the runtime image.

Network access remains manifest-owned. List every exact runtime destination in
`runtime.egress`, then create one allowlisted transport for the automation:

```python
from werkt.connectors import HTTPClient, UrllibTransport, Zoom

client = HTTPClient(UrllibTransport({
    "zoom.us",
    "api.zoom.us",
    "us02web.zoom.us",
}))
zoom = Zoom(
    account_id,
    client_id,
    client_secret,
    allowed_download_hosts={"us02web.zoom.us"},
    client=client,
)
```

Every request that transport makes says who it is: `Werkt-Automation/1.0`.
Sending nothing leaves urllib to name Python and its version, which the edges
in front of ordinary websites answer with 403 while serving any client that
identifies itself, and a connector reading a public page cannot tell that
refusal apart from a page that is not published yet. The agent names the
platform and nothing else, because an automation id is free text and
disclosing it would tell every destination something about the operator that
answering the request never required. Pass a `User-Agent` header, or
`UrllibTransport(hosts, user_agent=...)`, when a host expects a particular
name; either one is sent exactly as given, empty included.

Built-in connectors expose declarative `ConnectorSpec` metadata through
`BUILTIN_CONNECTORS`. The metadata identifies configuration, secret fields, and
fixed hosts, when applicable, without coupling connector behavior to one
automation.

## Connector testing

Use `ScriptedTransport` to test all outbound calls without opening a socket:

```python
from werkt.connectors import HTTPClient, HTTPResponse
from werkt.connectors.testing import ScriptedTransport

transport = ScriptedTransport([
    HTTPResponse(200, {"Content-Type": "application/json"}, b'{"ok":true}'),
])
client = HTTPClient(transport)
assert client.json("GET", "https://api.example.test/status") == {"ok": True}
transport.assert_finished()
```

Assert recorded methods, URLs, headers, and bodies when a connector's request
shape matters. Never put real credentials in fixtures.
