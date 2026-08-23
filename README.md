# Werkt

An agent-first, Git-native control plane for code automations. Triggers produce a durable, language-neutral event; immutable automation revisions consume it through a small process protocol. Automation code can run locally for development or inside a disposable [Husker](https://github.com/rvben/husker) microVM.

It runs. It is organized. It Werkt.

This is an executable MVP, not yet a production sandbox.

## What works

- Strict `automation.yaml` manifests with deterministic content hashes
- Durable, idempotent deployment jobs with ordered promotion checks, retained diagnostics, cancellation, retry, and rollback
- PostgreSQL-backed events, run queue, retries, worker leases, and concurrency policies
- Optional transactional per-automation JSON state for idempotency and small state machines
- Cron schedules with IANA time zones
- HMAC-authenticated webhook triggers with idempotency keys
- Bearer-authenticated RFC 5322 email ingestion
- ntfy subscriptions using its streaming JSON API
- A language-neutral execution contract with local-process and Husker backends
- Bearer-protected management API for external agents and operators
- Responsive management workspace at `/app/`, backed only by that public API
- Filtered inventory, deployment progress, automation detail, pause/resume, manual runs, and audit history
- AES-256-GCM secret vault with safe rotation, revision bindings, and runtime log redaction
- Python, Rust, and Go examples
- HTTP endpoints for health, automations, hooks, and run history
- CLI commands for validation, deployment, serving, and inspection

## Quick start

Start PostgreSQL:

```bash
docker compose up -d postgres
```

Validate the Python example:

```bash
go run ./cmd/werkt validate ./examples/python-hello
```

Generate a persistent 32-byte master key, then start the control plane, deployment worker, and two local run workers. Keep this key outside PostgreSQL and reuse it after restarts:

```bash
export WERKT_SECRET_KEY="$(openssl rand -base64 32)"
go run ./cmd/werkt serve -workers 2
```

From another terminal, create the named credentials without placing their values in command-line arguments, then upload the package. The CLI waits while Werkt validates its secret bindings, builds, and atomically activates the revision:

```bash
export PYTHON_HELLO_WEBHOOK_SECRET='development-webhook-secret-change-me'
export PYTHON_HELLO_EMAIL_TOKEN='development-email-token-change-me-now'
go run ./cmd/werkt secret set --from-env PYTHON_HELLO_WEBHOOK_SECRET examples/python-hello/webhook
go run ./cmd/werkt secret set --from-env PYTHON_HELLO_EMAIL_TOKEN examples/python-hello/email
go run ./cmd/werkt deploy ./examples/python-hello
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

Review retained deployment sources and inactive artifacts without deleting anything, then explicitly apply the short-lived plan:

```bash
go run ./cmd/werkt retention plan
go run ./cmd/werkt retention get ret_...
go run ./cmd/werkt retention apply ret_...
```

The defaults keep retryable sources for 30 days, inactive artifacts for 90 days, the newest three retryable sources per automation, and the newest five inactive revisions. Active work and active revisions are never candidates.

Deploying the Rust example runs its `cargo build --release` build command once. With the Husker backend, that build runs in the manifest's disposable `rust:1.88-bookworm` build VM; the resulting workspace becomes the immutable artifact:

```bash
export RUST_HELLO_WEBHOOK_SECRET='development-rust-secret-change-me-now'
go run ./cmd/werkt secret set --from-env RUST_HELLO_WEBHOOK_SECRET examples/rust-hello/webhook
go run ./cmd/werkt deploy ./examples/rust-hello
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
      secret: infrastructure/process-alert/webhook
runtime:
  language: python
  image: python:3.13-alpine
  command: [python3, main.py]
  egress:
    - host: incidents.example.com
      port: 443
  secrets:
    INCIDENT_API_TOKEN: infrastructure/process-alert/incident-api
deployment:
  checks:
    - id: syntax
      command: [python3, -m, py_compile, main.py]
      timeout: 1m
execution:
  timeout: 5m
  retries: 3
  concurrency: forbid
```

`runtime.language` is descriptive. The actual runtime contract is `runtime.command`, so any executable language works. `deployment.checks` uses the same language-neutral command-array contract: checks run in order after the optional build, each with a stable ID and timeout. `runtime.image` names the Husker rootfs catalog entry or OCI reference that provides those commands. When `runtime.build` is present, `runtime.buildImage` can select a separate toolchain image; otherwise the runtime image is reused. Both image fields are ignored by the local process executor. A daemon-wide `WERKT_HUSKER_ROOTFS` can be used as a fallback.

Runtime networking is also manifest-owned. With no `runtime.egress`, the Husker
VM has no network device. Each egress entry opens exactly one hostname, TCP or
UDP protocol (TCP by default), and port; Husker resolves and pins its IPv4
addresses before boot and denies everything else. The process executor rejects
manifests with egress policy because it cannot enforce that boundary.

Trigger credentials and `runtime.secrets` contain named vault references only. Werkt encrypts values before PostgreSQL, resolves only the names required at ingress or immediately before a run, and redacts resolved values from stdout/stderr before persistence. See [docs/security.md](docs/security.md) for key custody, signing, token, and redaction details.

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
| `WERKT_API_URL` | `http://127.0.0.1:8080`; management API used by `werkt deploy` |
| `WERKT_DATABASE_URL` | `postgres://automations:automations@localhost:54329/automations?sslmode=disable` |
| `WERKT_DATA_DIR` | `./data` |
| `WERKT_LISTEN_ADDR` | `127.0.0.1:8080` |
| `WERKT_MANAGEMENT_TOKEN` | empty; disables management authentication for local development |
| `WERKT_SECRET_KEY` | empty; base64-encoded 32-byte AES master key; secret operations fail closed when absent |
| `WERKT_WORKER_POLL` | `500ms` |
| `WERKT_SCHEDULER_POLL` | `1s` |
| `WERKT_SHUTDOWN_PERIOD` | `10s` |
| `WERKT_DEPLOY_TIMEOUT` | `30m` |
| `WERKT_DEPLOYMENT_POLL` | `500ms` |
| `WERKT_MAX_PACKAGE_BYTES` | `67108864` (64 MiB compressed) |
| `WERKT_MAX_EXPANDED_PACKAGE_BYTES` | `268435456` (256 MiB extracted) |
| `WERKT_MAX_PACKAGE_ENTRIES` | `10000` |
| `WERKT_EXECUTOR` | `process` |
| `WERKT_HUSKER_URL` | `http://127.0.0.1:8081` |
| `WERKT_HUSKER_TOKEN` | empty |
| `WERKT_HUSKER_ROOTFS` | empty; fallback when `runtime.image` is omitted |
| `WERKT_HUSKER_KERNEL` | empty; use the Husker daemon default |
| `WERKT_HUSKER_VCPUS` | `1` |
| `WERKT_HUSKER_MEMORY_MIB` | `256` |
| `WERKT_HUSKER_BUILD_NETWORK` | `nat` |
| `WERKT_HUSKER_BUILD_TIMEOUT` | `15m` |
| `WERKT_HUSKER_PROVISION_TIMEOUT` | `2m` |
| `WERKT_HUSKER_CLEANUP_TIMEOUT` | `30s` |

Webhook `secret`, email/ntfy `tokenSecret`, and `runtime.secrets` values are lowercase hierarchical names in the Werkt vault. Their plaintext values never appear in manifests, API responses, audit details, or PostgreSQL rows.

## Running through Husker

Set `WERKT_EXECUTOR=husker`, point `WERKT_HUSKER_URL` at the daemon, and set its bearer token when authentication is enabled. Each attempt gets a fresh VM, an immutable artifact upload, the same event/result protocol, and a hard server-side expiration. Werkt requests `network: none` when `runtime.egress` is empty and `network: filtered` with the exact policy otherwise. Husker versions that predate filtered networking reject that distinct mode instead of silently running unrestricted. Werkt destroys the VM after collecting the result; the Husker deadline is the cleanup fallback if a worker crashes.

Deployments with `runtime.build` get a separate short-lived builder VM. Builds default to NAT so package managers can fetch dependencies, while runtime VMs remain offline. Werkt uploads the source, invokes the build command directly without a shell, downloads the result in bounded ranges, rejects links and unsafe archive paths, and atomically promotes the completed workspace. Set `WERKT_HUSKER_BUILD_NETWORK=none` for fully vendored builds.

Warm pools are deliberately not used yet: Husker's current snapshot-fork pool path is NAT-only, while Werkt's safe default is no guest network. Cold one-shot VMs preserve the intended security boundary until isolated pool forks exist.

See [docs/execution.md](docs/execution.md) for the complete boundary and failure semantics.

## Current trust boundary

The `process` executor runs builds and deployed commands as child processes on the control-plane host and is for trusted local development only. Build and runtime children receive an allowlisted base environment; runtime attempts additionally receive only their explicit vault mappings. This protects Werkt credentials from accidental inheritance but does not sandbox host filesystem or network access, so the executor fails deployment and execution when a manifest requires egress enforcement. The `husker` executor isolates both builds and runtime code in separate microVMs, but Husker is currently a single-host, single-trust-domain system rather than a hostile multi-tenant service. Signed artifacts, dependency caches, build-plane egress allowlists, and multi-key master-key rotation remain production-hardening work.
