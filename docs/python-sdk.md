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
