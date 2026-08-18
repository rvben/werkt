# Werkt

An agent-first, Git-native control plane for code automations. Triggers produce a durable, language-neutral event; immutable automation revisions consume it through a small process protocol. Automation code can run locally for development or inside a disposable [Husker](https://github.com/rvben/husker) microVM.

It runs. It is organized. It Werkt.

This is an executable MVP, not yet a production sandbox.

## What works

- Strict `automation.yaml` manifests with deterministic content hashes
- Immutable deployment artifacts
- PostgreSQL-backed events, run queue, retries, worker leases, and concurrency policies
- Cron schedules with IANA time zones
- Webhook triggers with idempotency keys
- RFC 5322 email ingestion for mail-provider or forwarding integrations
- ntfy subscriptions using its streaming JSON API
- A language-neutral execution contract with local-process and Husker backends
- Bearer-protected management API for external agents and operators
- Filtered inventory, automation detail, pause/resume, manual runs, and audit history
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
go run ./cmd/werkt serve -workers 2
```

Invoke its webhook from another terminal:

```bash
curl -i \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: getting-started-1' \
  -d '{"name":"Ruben"}' \
  http://localhost:8080/api/v1/hooks/python-hello/incoming
```

An email provider or forwarding service can post a raw message to an `email` trigger:

```bash
curl --data-binary $'Message-Id: <example-1@example.com>\nFrom: sender@example.com\nTo: automations@example.com\nSubject: Hello\n\nEmail body' \
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

See [docs/management-api.md](docs/management-api.md) for the full agent-facing contract.

Deploying the Rust example runs its `cargo build --release` build command once. With the Husker backend, that build runs in the manifest's disposable `rust:1.88-bookworm` build VM; the resulting workspace becomes the immutable artifact:

```bash
go run ./cmd/werkt deploy ./examples/rust-hello
curl -d '{"from":"rust"}' http://localhost:8080/api/v1/hooks/rust-hello/incoming
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
runtime:
  language: python
  image: python:3.13-alpine
  command: [python3, main.py]
execution:
  timeout: 5m
  retries: 3
  concurrency: forbid
```

`runtime.language` is descriptive. The actual contract is `runtime.command`, so any executable language works. `runtime.image` names the Husker rootfs catalog entry or OCI reference that provides that command. When `runtime.build` is present, `runtime.buildImage` can select a separate toolchain image; otherwise the runtime image is reused. Both image fields are ignored by the local process executor. A daemon-wide `WERKT_HUSKER_ROOTFS` can be used as a fallback.

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

An ntfy trigger accepts `server`, `topic`, and an optional `tokenEnv`. The token is read from the control-plane environment and is not stored in the manifest.

## Running through Husker

Set `WERKT_EXECUTOR=husker`, point `WERKT_HUSKER_URL` at the daemon, and set its bearer token when authentication is enabled. Each attempt gets a fresh VM, an immutable artifact upload, the same event/result protocol, and a hard server-side expiration. Werkt requests `network: none` by default and destroys the VM after collecting the result. The Husker deadline is the cleanup fallback if a worker crashes.

Deployments with `runtime.build` get a separate short-lived builder VM. Builds default to NAT so package managers can fetch dependencies, while runtime VMs remain offline. Werkt uploads the source, invokes the build command directly without a shell, downloads the result in bounded ranges, rejects links and unsafe archive paths, and atomically promotes the completed workspace. Set `WERKT_HUSKER_BUILD_NETWORK=none` for fully vendored builds.

Warm pools are deliberately not used yet: Husker's current snapshot-fork pool path is NAT-only, while Werkt's safe default is no guest network. Cold one-shot VMs preserve the intended security boundary until isolated pool forks exist.

See [docs/execution.md](docs/execution.md) for the complete boundary and failure semantics.

## Current trust boundary

The `process` executor runs builds and deployed commands as child processes on the control-plane host and is for trusted local development only. The `husker` executor isolates both builds and runtime code in separate microVMs, but Husker is currently a single-host, single-trust-domain system rather than a hostile multi-tenant service. Secret injection, signed artifacts, dependency caches, and stronger outbound allowlists remain production-hardening work.
