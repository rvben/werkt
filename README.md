# Werkt

An agent-first, Git-native control plane for code automations. Triggers produce a durable, language-neutral event; immutable automation revisions consume it through a small process protocol. Automation code can run locally for development or inside a disposable [Husker](https://github.com/rvben/husker) microVM.

It runs. It is organized. It Werkt.

This is an executable MVP, not yet a production sandbox.

## What works

- Strict `automation.yaml` manifests with deterministic content hashes
- Immutable deployment artifacts
- PostgreSQL-backed events, run queue, retries, worker leases, and concurrency policies
- Cron schedules with IANA time zones
- HMAC-authenticated webhook triggers with idempotency keys
- Bearer-authenticated RFC 5322 email ingestion
- ntfy subscriptions using its streaming JSON API
- A language-neutral execution contract with local-process and Husker backends
- Bearer-protected management API for external agents and operators
- Responsive management workspace at `/app/`, backed only by that public API
- Filtered inventory, automation detail, pause/resume, manual runs, and audit history
- Explicit runtime secret mapping without persisted secret values
- Python, Rust, and Go examples
- HTTP endpoints for health, automations, hooks, and run history
- CLI commands for validation, deployment, serving, and inspection

## Quick start

Start PostgreSQL:

```bash
docker compose up -d postgres
```

Validate and deploy the Python example:

```bash
go run ./cmd/werkt validate ./examples/python-hello
go run ./cmd/werkt deploy ./examples/python-hello
```

Start the control plane and two local workers:

```bash
export PYTHON_HELLO_WEBHOOK_SECRET='development-webhook-secret-change-me'
export PYTHON_HELLO_EMAIL_TOKEN='development-email-token-change-me-now'
go run ./cmd/werkt serve -workers 2
```

Open [http://localhost:8080/app/](http://localhost:8080/app/) to use the management workspace. If `WERKT_MANAGEMENT_TOKEN` is set, connect with the same token used by API clients; it remains scoped to the browser tab. The workspace does not have a privileged control path and attributes its mutations as `workspace:operator`.

Invoke its webhook from another terminal:

```bash
payload='{"name":"Ruben"}'
timestamp="$(date +%s)"
idempotency_key='getting-started-1'
signature="$(printf '%s' "$timestamp.$idempotency_key.$payload" | openssl dgst -sha256 -hmac "$PYTHON_HELLO_WEBHOOK_SECRET" -hex | awk '{print $2}')"
curl -i \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $idempotency_key" \
  -H "X-Werkt-Timestamp: $timestamp" \
  -H "X-Werkt-Signature: sha256=$signature" \
  --data-binary "$payload" \
  http://localhost:8080/api/v1/hooks/python-hello/incoming
```

An email provider or forwarding service can post a raw message to an `email` trigger:

```bash
curl -H "Authorization: Bearer $PYTHON_HELLO_EMAIL_TOKEN" \
  --data-binary $'Message-Id: <example-1@example.com>\nFrom: sender@example.com\nTo: automations@example.com\nSubject: Hello\n\nEmail body' \
  http://localhost:8080/api/v1/email/python-hello/mail
```

Inspect runs:

```bash
go run ./cmd/werkt runs
curl http://localhost:8080/api/v1/runs
```

For a non-local deployment, configure a management token:

```bash
export WERKT_MANAGEMENT_TOKEN='replace-with-a-secret'
curl -H "Authorization: Bearer $WERKT_MANAGEMENT_TOKEN" \
  'http://localhost:8080/api/v1/automations?project=examples&enabled=true'
```

Pause an automation without disabling manual diagnosis, then queue an idempotent manual run:

```bash
curl -X PATCH -H "Authorization: Bearer $WERKT_MANAGEMENT_TOKEN" \
  -H 'X-Werkt-Actor: agent:operator' -H 'Content-Type: application/json' \
  -d '{"enabled":false}' http://localhost:8080/api/v1/automations/go-hello

curl -H "Authorization: Bearer $WERKT_MANAGEMENT_TOKEN" \
  -H 'X-Werkt-Actor: agent:operator' -H 'Idempotency-Key: diagnostic-1' \
  -H 'Content-Type: application/json' -d '{"reason":"diagnostic"}' \
  http://localhost:8080/api/v1/automations/go-hello/runs
```

See [docs/management-api.md](docs/management-api.md) for the full agent-facing contract and the workspace/API relationship.

Deploying the Rust example runs its `cargo build --release` build command once. With the Husker backend, that build runs in the manifest's disposable `rust:1.88-bookworm` build VM; the resulting workspace becomes the immutable artifact:

```bash
go run ./cmd/werkt deploy ./examples/rust-hello
export RUST_HELLO_WEBHOOK_SECRET='development-rust-secret-change-me-now'
payload='{"from":"rust"}'
timestamp="$(date +%s)"
idempotency_key='rust-example-1'
signature="$(printf '%s' "$timestamp.$idempotency_key.$payload" | openssl dgst -sha256 -hmac "$RUST_HELLO_WEBHOOK_SECRET" -hex | awk '{print $2}')"
curl -H "Idempotency-Key: $idempotency_key" -H "X-Werkt-Timestamp: $timestamp" \
  -H "X-Werkt-Signature: sha256=$signature" --data-binary "$payload" \
  http://localhost:8080/api/v1/hooks/rust-hello/incoming
```

## Automation package

```yaml
apiVersion: werkt.dev/v1
kind: Automation
metadata:
  name: process-alert
  project: infrastructure
  folder: alerts
  labels: [production, critical]
triggers:
  - id: nightly
    type: schedule
    config:
      cron: "0 2 * * *"
      timezone: Europe/Amsterdam
  - id: incoming
    type: webhook
    config:
      secretEnv: PROCESS_ALERT_WEBHOOK_SECRET
runtime:
  language: python
  image: python:3.13-alpine
  command: [python3, main.py]
  secrets:
    INCIDENT_API_TOKEN: PROCESS_ALERT_INCIDENT_API_TOKEN
execution:
  timeout: 5m
  retries: 3
  concurrency: forbid
```

`runtime.language` is descriptive. The actual contract is `runtime.command`, so any executable language works. `runtime.image` names the Husker rootfs catalog entry or OCI reference that provides that command. When `runtime.build` is present, `runtime.buildImage` can select a separate toolchain image; otherwise the runtime image is reused. Both image fields are ignored by the local process executor. A daemon-wide `WERKT_HUSKER_ROOTFS` can be used as a fallback.

Trigger credentials and `runtime.secrets` contain environment-variable references only. Values are resolved at ingress or immediately before a run and never stored in PostgreSQL. See [docs/security.md](docs/security.md) for signing, token, and isolation details.

## Process protocol

The runner sets these variables for every run:

- `WERKT_AUTOMATION_ID`
- `WERKT_REVISION_ID`
- `WERKT_RUN_ID`
- `WERKT_EVENT_PATH`
- `WERKT_RESULT_PATH`

The program reads the event envelope from `WERKT_EVENT_PATH`, writes a JSON result to `WERKT_RESULT_PATH`, logs to stdout/stderr, and exits non-zero on failure. If it produces no result file, the result defaults to `{}`.

The event envelope is stable across every trigger and runtime:

```json
{
  "id": "evt_...",
  "occurredAt": "2026-08-17T10:30:00Z",
  "receivedAt": "2026-08-17T10:30:00Z",
  "trigger": {
    "automation": "process-alert",
    "id": "incoming",
    "type": "webhook"
  },
  "data": {},
  "metadata": {}
}
```

## Configuration

| Environment variable | Default |
|---|---|
| `WERKT_DATABASE_URL` | `postgres://automations:automations@localhost:54329/automations?sslmode=disable` |
| `WERKT_DATA_DIR` | `./data` |
| `WERKT_LISTEN_ADDR` | `127.0.0.1:8080` |
| `WERKT_MANAGEMENT_TOKEN` | empty; disables management authentication for local development |
| `WERKT_WORKER_POLL` | `500ms` |
| `WERKT_SCHEDULER_POLL` | `1s` |
| `WERKT_SHUTDOWN_PERIOD` | `10s` |
| `WERKT_DEPLOY_TIMEOUT` | `30m` |
| `WERKT_EXECUTOR` | `process` |
| `WERKT_HUSKER_URL` | `http://127.0.0.1:8081` |
| `WERKT_HUSKER_TOKEN` | empty |
| `WERKT_HUSKER_ROOTFS` | empty; fallback when `runtime.image` is omitted |
| `WERKT_HUSKER_KERNEL` | empty; use the Husker daemon default |
| `WERKT_HUSKER_VCPUS` | `1` |
| `WERKT_HUSKER_MEMORY_MIB` | `256` |
| `WERKT_HUSKER_NETWORK` | `none` |
| `WERKT_HUSKER_BUILD_NETWORK` | `nat` |
| `WERKT_HUSKER_BUILD_TIMEOUT` | `15m` |
| `WERKT_HUSKER_PROVISION_TIMEOUT` | `2m` |
| `WERKT_HUSKER_CLEANUP_TIMEOUT` | `30s` |

Webhook `secretEnv`, email `tokenEnv`, ntfy `tokenEnv`, and runtime secret references are read from the control-plane environment and are not stored as values in the manifest or database.

## Running through Husker

Set `WERKT_EXECUTOR=husker`, point `WERKT_HUSKER_URL` at the daemon, and set its bearer token when authentication is enabled. Each attempt gets a fresh VM, an immutable artifact upload, the same event/result protocol, and a hard server-side expiration. Werkt requests `network: none` by default and destroys the VM after collecting the result. The Husker deadline is the cleanup fallback if a worker crashes.

Deployments with `runtime.build` get a separate short-lived builder VM. Builds default to NAT so package managers can fetch dependencies, while runtime VMs remain offline. Werkt uploads the source, invokes the build command directly without a shell, downloads the result in bounded ranges, rejects links and unsafe archive paths, and atomically promotes the completed workspace. Set `WERKT_HUSKER_BUILD_NETWORK=none` for fully vendored builds.

Warm pools are deliberately not used yet: Husker's current snapshot-fork pool path is NAT-only, while Werkt's safe default is no guest network. Cold one-shot VMs preserve the intended security boundary until isolated pool forks exist.

See [docs/execution.md](docs/execution.md) for the complete boundary and failure semantics.

## Current trust boundary

The `process` executor runs builds and deployed commands as child processes on the control-plane host and is for trusted local development only. Runtime children receive an allowlisted base environment plus explicit secret mappings, but local build commands still inherit the host environment. The `husker` executor isolates both builds and runtime code in separate microVMs, but Husker is currently a single-host, single-trust-domain system rather than a hostile multi-tenant service. A dedicated secret backend with rotation and log redaction, signed artifacts, dependency caches, and stronger outbound allowlists remain production-hardening work.
