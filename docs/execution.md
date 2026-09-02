# Execution boundary

Werkt owns automation definitions, triggers, durable events, revision history, queueing, retries, concurrency, and run results. Executors own the mechanics and isolation of one attempt. Husker is an execution plane, not a second scheduler.

## Deployment lifecycle

Agents upload a deterministic package to the management API. Werkt verifies the digest while streaming the bounded body to disk, safely extracts it into private staging, and records an idempotent `queued` deployment. A lease-backed worker then advances it through `validating`, `building`, and `activating`. Expired leases can be reacquired after a worker crash.

The source package stays private to the control plane and is retained after a terminal result so an operator or agent can retry the exact same bytes without uploading a subtly different package. Policy-driven retention later expires old material through a persisted dry-run/apply workflow. Before publication, Werkt hashes the artifact tree and signs a statement binding it to the source and effective execution environment. Activation stores that provenance with the immutable revision, replaces effective triggers, and marks the deployment successful in one transaction, so clients cannot observe an active revision paired with a failed or unfinished job.

When `runtime.tools` is present, validation resolves its exact versions through
the built-in catalog. Werkt verifies a local Husker base-image digest and a
host-provisioned mise binary digest, uploads mise into a secret-free preparation
VM, installs only catalogued tools with filtered egress, verifies the guest
platform and tool capabilities, stops the VM, and promotes its root filesystem
through Husker's image API. The logical inputs and resulting image digest are
folded into the revision hash and signed provenance before activation. A repeat
deployment reuses only an image with the expected parent and digest.

## Build lifecycle

When a manifest defines `runtime.build` or `deployment.checks`, deployment copies the source into a private staging directory before invoking the selected builder. Checks are ordered command arrays with stable IDs and independent timeouts. They run after the optional build and must all pass before activation. The process backend invokes these commands on the Werkt host for trusted local development only. The Husker backend instead:

1. Verifies the revision's prepared `runtime.tools` image digest, or ensures the digest-pinned image escape hatch is present, then creates a fresh VM from it.
2. Gives the VM an independent hard expiration and `owner: werkt/build/<automation-id>`.
3. Uploads and extracts the source, then invokes the build and check arrays directly without a shell in the same disposable VM.
4. Archives the completed guest workspace and downloads it using bounded ranged reads.
5. Extracts into a second staging directory, rejecting absolute paths, traversal, links, and special files.
6. Atomically replaces the unbuilt staging tree and destroys the builder VM. Only then can deployment publish the content-addressed revision.

Builder networking defaults to `nat` because dependency resolution commonly needs outbound access. Runtime networking remains independently set to `none`. Fully vendored builds can set `WERKT_HUSKER_BUILD_NETWORK=none`. `runtime.buildImage` should contain the compiler and `/bin/tar`; `runtime.image` only needs the final runtime and `/bin/tar`.

Validation, build, every check, and activation are durable deployment steps with timestamps, bounded logs, and a terminal error. Cancellation is immediate for queued work and cooperative for active commands; the worker context terminates the local process or guest request. Failed and cancelled jobs can be retried from their retained source. Rollback is different: it atomically makes an already-published immutable revision active again and reconstructs its trigger set without rebuilding code.

## Storage retention

Werkt stores extracted deployment sources under `deployment-sources` and content-addressed runtime artifacts under `artifacts`. Retention plans group shared paths before making decisions, so a retry chain that references one source is treated as one storage item. Age and per-automation count rules are additive protections: storage must exceed both before it becomes a candidate.

Apply re-runs candidate selection and then performs a final database-side protection check. Database references are detached before an owned direct-child path is removed, preventing future work from discovering storage during deletion. Active sources, active revisions, artifacts being reused by in-progress deployments, and artifacts needed by queued or running runs cannot be detached. Historical rows, deployment diagnostics, revision manifests, and audit events remain in PostgreSQL after their filesystem material is pruned.

Each runnable revision also owns immutable bindings to its named secrets. Artifact pruning releases those bindings only after the same final protection checks, because a revision without an artifact can no longer execute or be rolled back. This lets operators delete credentials that are referenced only by pruned history without weakening active revision safety.

## Stable automation contract

Every executor starts `runtime.command` in the deployed artifact directory and provides:

- `WERKT_AUTOMATION_ID`
- `WERKT_REVISION_ID`
- `WERKT_RUN_ID`
- `WERKT_EVENT_PATH`, containing the normalized event envelope
- `WERKT_RESULT_PATH`, initialized to `{}` and expected to contain valid JSON at exit
- `WERKT_CONTROL_PATH`, initialized to `{}` and accepting one durable continuation request

When `execution.state.enabled` is true, Werkt also provides:

- `WERKT_STATE_PATH`, initialized with the automation's latest committed JSON object
- `WERKT_STATE_VERSION`, the decimal version of that snapshot

The automation may replace the state file with another JSON object of at most
64 KiB. Werkt reads it only after a zero exit and commits it in the same
database transaction that marks the run successful. Failed attempts never
advance state. A compare-and-swap on the supplied version prevents a stale
attempt from overwriting a newer transition. Stateful manifests must use
`execution.concurrency: forbid`; this keeps external side effects and state
transitions serialized instead of pretending they can be rolled back together.
State is durable operational data, not a secret store: credentials still belong
in the vault.

For `runtime.language: python`, Werkt injects its dependency-free authoring SDK
under the reserved `.werkt-sdk/python` artifact path and sets `PYTHONPATH` for
deployment build commands, checks, and run attempts. The SDK digest is folded
into the revision's content hash. Packages therefore cannot shadow the managed
SDK path, and SDK changes create new immutable revisions instead of altering
existing artifacts. The underlying files and environment variables remain the
portable contract for other languages.

After a successful attempt, an automation may write run control of at most 64
KiB. A deferred continuation is pinned to the same immutable revision
and becomes runnable at the declared RFC 3339 time, up to 30 days ahead:

```json
{"defer":{"key":"recording-42.poll","until":"2026-08-31T12:00:00Z","data":{"recordingId":"42"}}}
```

An approval creates a typed operator task. Fields may be `text`,
`textarea`, `number`, `boolean`, or `select`; actions are `approve` or `reject`.
The eventual decision queues a pinned `approval` event rather than mutating or
re-running the requesting attempt:

```json
{"approval":{"key":"recording-42.publish","title":"Publish recording?","expiresAt":"2026-08-31T18:00:00Z","fields":[{"id":"title","label":"Title","type":"text","required":true}],"actions":[{"id":"approve","label":"Publish","style":"primary","requiresFields":true},{"id":"reject","label":"Skip","style":"neutral"}]}}
```

An automation may request both an approval and one deferred continuation in the
same control object. This is intended for a durable expiry or escalation step:
the approval and timer are inserted atomically, and the deferred run must treat
an already-resolved approval as a stale no-op.

An automation may also request up to eight provider-neutral operator messages.
Keys must be unique within the run, text is bounded, and priority is `low`,
`default`, or `high`. Werkt, rather than automation code, selects destinations,
holds provider credentials, attaches the run link, retries delivery, and audits
the outcome:

```json
{"notifications":[{"key":"recording.started","title":"Recording started","body":"The Sunday service recording has started.","priority":"default"}]}
```

Control is read and persisted only after a zero exit, in the same database
transaction as the run result and state commit. A failed attempt therefore
cannot leave behind a timer, approval, or notification. Malformed data, past or overly distant
times, duplicate fields, or undeclared action shapes fail the attempt instead
of guessing intent.

Standard output and error are redacted against the exact secrets resolved for that attempt, then bounded to 1 MiB and persisted as run logs. A non-zero exit fails the attempt. Exit code 124 is treated as a timeout. The runtime language is not part of this protocol.

## Husker attempt lifecycle

1. Werkt derives a collision-resistant VM name from the run ID and attempt.
2. It verifies the Ed25519 attestation and current artifact-tree digest before crossing the execution boundary.
3. It verifies the prepared `runtime.tools` image digest (or resolves the pinned OCI escape hatch), then creates a VM from it with the requested resources, a manifest-derived network policy, `owner: werkt/<run-id>`, and a hard lifetime covering provisioning, execution, and cleanup grace.
4. It waits for the guest agent, uploads the compressed immutable artifact in bounded chunks, and extracts it into a fresh guest directory.
5. It uploads the event, initializes the result, and invokes `runtime.command` without a shell.
6. It collects bounded logs and the JSON result, then destroys the VM using a cleanup context independent of the run context.
7. If the worker or control plane disappears, Husker's durable expiration reaper destroys the VM.

The runtime network policy is part of the immutable manifest. An empty
`runtime.egress` produces `network: none`. A non-empty policy produces
`network: filtered` and exact hostname, protocol, and port entries. TCP is the
default protocol. Husker resolves hostnames and pins their IPv4 addresses before
boot, permits only the configured gateway and DNS infrastructure around those
destinations, and denies other IPv4, IPv6, and layer-2 traffic. The distinct
`filtered` mode is a compatibility guard: an older daemon rejects the request
instead of ignoring the unknown rules and granting unrestricted NAT.

The process executor cannot enforce network policy and therefore rejects a
manifest that declares `runtime.egress`. Runtime egress is intentionally not a
worker-wide switch. Build networking remains separately operator-controlled
through `WERKT_HUSKER_BUILD_NETWORK` because dependency resolution occurs before
the runtime artifact boundary.

## Security properties and non-goals

- Artifact paths are relative tar entries; symlinks and special files are rejected.
- API uploads are chunked below Husker's default request and file-write ceilings.
- Build artifacts and runtime results are downloaded in bounded ranges and checked for size or modification changes between chunks.
- Husker bearer credentials stay in the worker configuration and are never exposed to automation code.
- Only manifest environment values, explicitly named vault values, and reserved Werkt protocol values are sent to the guest.
- Transactional automation state is bounded to a 64 KiB JSON object, is never
  committed from a failed attempt, and cannot be updated by a worker that lost
  its run lease.
- Runtime VMs are offline unless their immutable manifest contains explicit
  egress destinations; policies cannot be silently downgraded to plain NAT.
- The VM deadline is activity-independent. It remains effective during stuck commands and after orchestrator failure.
- Husker's `owner` field is correlation metadata, not an authorization boundary.
- Artifact attestations use an installation-scoped key derived from `WERKT_SECRET_KEY`; independent transparency-log publication is not yet implemented.
- Current Husker authentication represents one trust domain. Per-tenant authorization and quotas are future work.

## Pooling

Husker pools currently restore NAT-mode Firecracker snapshots. Werkt does not silently trade away its no-network default for faster startup, so this backend cold-boots one-shot VMs. Pool checkout can be introduced after Husker supports isolated forks and the same durable expiration metadata on checkout.
