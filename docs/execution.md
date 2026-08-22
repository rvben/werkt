# Execution boundary

Werkt owns automation definitions, triggers, durable events, revision history, queueing, retries, concurrency, and run results. Executors own the mechanics and isolation of one attempt. Husker is an execution plane, not a second scheduler.

## Deployment lifecycle

Agents upload a deterministic package to the management API. Werkt verifies the digest while streaming the bounded body to disk, safely extracts it into private staging, and records an idempotent `queued` deployment. A lease-backed worker then advances it through `validating`, `building`, and `activating`. Expired leases can be reacquired after a worker crash.

The source package stays private to the control plane and is removed when the job reaches `succeeded` or `failed`. Activation publishes the immutable revision, replaces effective triggers, and marks the deployment successful in one transaction, so clients cannot observe an active revision paired with a failed or unfinished job.

## Build lifecycle

When a manifest defines `runtime.build`, deployment copies the source into a private staging directory before invoking the selected builder. The process backend invokes the command on the Werkt host for trusted local development only. The Husker backend instead:

1. Creates a fresh VM from `runtime.buildImage`, falling back to `runtime.image` and then the daemon-wide rootfs setting.
2. Gives the VM an independent hard expiration and `owner: werkt/build/<automation-id>`.
3. Uploads and extracts the source, then invokes the build array directly without a shell.
4. Archives the completed guest workspace and downloads it using bounded ranged reads.
5. Extracts into a second staging directory, rejecting absolute paths, traversal, links, and special files.
6. Atomically replaces the unbuilt staging tree and destroys the builder VM. Only then can deployment publish the content-addressed revision.

Builder networking defaults to `nat` because dependency resolution commonly needs outbound access. Runtime networking remains independently set to `none`. Fully vendored builds can set `WERKT_HUSKER_BUILD_NETWORK=none`. `runtime.buildImage` should contain the compiler and `/bin/tar`; `runtime.image` only needs the final runtime and `/bin/tar`.

## Stable automation contract

Every executor starts `runtime.command` in the deployed artifact directory and provides:

- `WERKT_AUTOMATION_ID`
- `WERKT_REVISION_ID`
- `WERKT_RUN_ID`
- `WERKT_EVENT_PATH`, containing the normalized event envelope
- `WERKT_RESULT_PATH`, initialized to `{}` and expected to contain valid JSON at exit

Standard output and error become bounded run logs. A non-zero exit fails the attempt. Exit code 124 is treated as a timeout. The runtime language is not part of this protocol.

## Husker attempt lifecycle

1. Werkt derives a collision-resistant VM name from the run ID and attempt.
2. It creates a VM from `runtime.image` (or the configured fallback) with the requested resources, explicit network mode, `owner: werkt/<run-id>`, and a hard lifetime covering provisioning, execution, and cleanup grace.
3. It waits for the guest agent, uploads the compressed immutable artifact in bounded chunks, and extracts it into a fresh guest directory.
4. It uploads the event, initializes the result, and invokes `runtime.command` without a shell.
5. It collects bounded logs and the JSON result, then destroys the VM using a cleanup context independent of the run context.
6. If the worker or control plane disappears, Husker's durable expiration reaper destroys the VM.

The default network mode is `none`. `nat` or `bridged` must be explicitly configured for the whole Werkt worker deployment. A later manifest-level egress policy should be allowlist-based rather than a boolean network switch.

## Security properties and non-goals

- Artifact paths are relative tar entries; symlinks and special files are rejected.
- API uploads are chunked below Husker's default request and file-write ceilings.
- Build artifacts and runtime results are downloaded in bounded ranges and checked for size or modification changes between chunks.
- Husker bearer credentials stay in the worker configuration and are never exposed to automation code.
- Only manifest environment values and reserved Werkt protocol values are sent to the guest.
- The VM deadline is activity-independent. It remains effective during stuck commands and after orchestrator failure.
- Husker's `owner` field is correlation metadata, not an authorization boundary.
- Build-image tags are not yet required to be immutable digests, and artifacts are not yet signed.
- Current Husker authentication represents one trust domain. Per-tenant authorization and quotas are future work.

## Pooling

Husker pools currently restore NAT-mode Firecracker snapshots. Werkt does not silently trade away its no-network default for faster startup, so this backend cold-boots one-shot VMs. Pool checkout can be introduced after Husker supports isolated forks and the same durable expiration metadata on checkout.
