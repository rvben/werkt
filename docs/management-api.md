# Management API

The management API is the stable control-plane surface for operators, external agents, and a future UI. It is distinct from trigger ingress: knowing a webhook URL does not grant access to inventory, run history, lifecycle changes, or manual execution.

The machine-readable OpenAPI 3.1 contract is served at `GET /api/openapi.yaml` and does not require authentication.

## Management workspace

`GET /app/` serves the responsive operator workspace embedded in the Werkt binary. It is deliberately a client of this management API rather than a separate administrative backend: deployment progress and diagnosis, inventory, detail, pause/resume, manual runs, run diagnosis, and audit history all use the endpoints documented below.

When management authentication is enabled, choose **Connection** and enter the same bearer token an external agent would use. The token is held in `sessionStorage`, is never rendered into the page or embedded asset, and is cleared when the browser tab closes. Workspace mutations send `X-Werkt-Actor: workspace:operator` so they remain attributable in the audit trail.

The workspace is a convenience for operators, not a requirement for automation or agent access. An API-only deployment remains fully supported.

## Authentication and attribution

Set `WERKT_MANAGEMENT_TOKEN` and send it as a bearer token on every management request:

```http
Authorization: Bearer <token>
X-Werkt-Actor: agent:operator
```

`X-Werkt-Actor` is recorded in the audit trail but is attribution supplied by the authenticated caller, not a separate identity proof. If omitted, it is recorded as `api`. When `WERKT_MANAGEMENT_TOKEN` is empty, management authentication is disabled for local development and Werkt emits a startup warning. Werkt binds to `127.0.0.1:8080` by default; explicitly configure both a token and `WERKT_LISTEN_ADDR` before exposing it beyond the host.

Health checks and trigger ingress (`/hooks/...` and `/email/...`) do not accept the management token as authority and remain outside this middleware. Trigger ingress has independent per-trigger credentials described in [security.md](security.md).

## Secrets

The management surface exposes encrypted-secret lifecycle without a reveal operation:

- `GET /api/v1/secrets` lists safe metadata.
- `GET /api/v1/secrets/{name}` returns safe metadata for one name.
- `PUT /api/v1/secrets/{name}` with `{"value":"...","description":"..."}` creates or rotates a value.
- `DELETE /api/v1/secrets/{name}` deletes only when no retained runnable revision references it.

Hierarchical names contain `/`; clients must percent-encode the name as one path segment. The Werkt client and CLI do this automatically. Responses contain only the name, optional description, monotonically increasing version, and timestamps. They never contain plaintext or ciphertext.

```bash
werkt secret set --from-env GITHUB_TOKEN infrastructure/process-alert/github
werkt secret list
werkt secret get infrastructure/process-alert/github
werkt secret delete infrastructure/process-alert/github
```

Create and rotation return `201` and `200` respectively. Invalid names or values return `400`; missing names return `404`; a retained revision binding returns `409`; and a server without `WERKT_SECRET_KEY` returns `503`. Mutations emit `secret.created`, `secret.rotated`, and `secret.deleted` audit events.

## Deployments

`POST /api/v1/deployments` accepts a gzip-compressed tar package and creates a durable asynchronous deployment. Supply all of these headers:

```http
Authorization: Bearer <token>
Content-Type: application/gzip
X-Werkt-Content-SHA256: <lowercase SHA-256 of the exact compressed body>
Idempotency-Key: <stable key for this intended deployment>
X-Werkt-Actor: agent:deployer
```

A new upload returns `202 Accepted`; an idempotent retry returns `200 OK` and the original deployment. The response includes `Location` for `GET /api/v1/deployments/{id}` and `Retry-After: 1` while the deployment is not terminal. Reusing an idempotency key for different package bytes returns `409 Conflict`.

The lifecycle is `queued` → `validating` → optional `building` → optional `checking` → `activating` → `succeeded`. Any active stage can become `failed` or `cancelled`. The deployment detail includes ordered validation, build, check, and activation steps with bounded logs, errors, and timings. A successful response includes the automation, content hash, and immutable revision. Revision activation and the transition to `succeeded` commit in the same database transaction.

`GET /api/v1/deployments` returns newest first and accepts `automation`, `status`, and `limit` filters. Agents should poll the resource named by `Location` until `succeeded`, `failed`, or `cancelled`; the CLI implements this contract:

```bash
WERKT_API_URL=https://werkt.example \
WERKT_MANAGEMENT_TOKEN="$WERKT_MANAGEMENT_TOKEN" \
werkt deploy --idempotency-key release-2026-08-22 ./automation
```

Cancel active work or retry the exact retained source of a failed/cancelled deployment:

```bash
werkt deployment cancel dep_...
werkt deployment retry dep_... --idempotency-key repair-2026-08-22
werkt deployment get dep_...
```

`POST /api/v1/deployments/{id}/cancel` is safe only for nonterminal work. `POST /api/v1/deployments/{id}/retry` requires `Idempotency-Key`, returns a new deployment linked by `retryOf`, and rejects successful or active jobs. A pruned source returns `409 Conflict` rather than silently accepting a retry that cannot run.

Package intake is streamed and bounded. Werkt rejects traversal paths, links, special files, duplicate case-insensitive names, excessive entries, and compressed or expanded bodies over the configured limits before any build runs.

## Retention plans

Retention is an explicit plan/apply workflow. Create a persisted dry run with `POST /api/v1/retention/plans`; the policy uses positive Go duration strings and per-automation count floors:

```json
{
  "sourceMaxAge": "720h",
  "artifactMaxAge": "2160h",
  "keepRetryableSources": 3,
  "keepInactiveRevisions": 5
}
```

An item is eligible only when it is older than its age limit and outside its count floor. Active deployment sources, active revision artifacts, artifacts being reused by an in-progress deployment, and artifacts referenced by queued or running runs are always protected. Successful deployment sources use the age limit because they are not retryable; failed and cancelled sources receive both protections.

Plans expire after 15 minutes and expose only relative `storageKey` values, never host filesystem paths. Applying `POST /api/v1/retention/plans/{id}/apply` re-evaluates every candidate before detaching database references and deleting storage. Items that became protected are recorded as `skipped`; deletion errors are recorded as `failed`. Apply is lease-protected and idempotent, and both planning and final outcomes emit audit events.

```bash
werkt retention plan --source-max-age 720h --artifact-max-age 2160h
werkt retention get ret_...
werkt retention apply ret_...
```

Artifact retention intentionally makes sufficiently old inactive revisions unavailable for rollback. A rollback to a pruned revision returns `409 Conflict`; its immutable metadata and history remain available.

## Automations

`GET /api/v1/automations` returns the organized inventory. Each summary includes the authoritative latest run identity, status, and creation time when the automation has run, so clients do not need to infer health from a bounded global history window. Optional filters are `project`, `folder`, `label`, `q`, and `enabled`.

`GET /api/v1/automations/{id}` returns metadata, the active manifest, effective trigger state, and revision history.

`PATCH /api/v1/automations/{id}` accepts one strict JSON shape:

```json
{"enabled": false}
```

Pausing blocks schedule, webhook, email, and ntfy ingestion. Manual runs remain available for diagnosis. Resuming recalculates each schedule from the next future occurrence, so missed intervals are not replayed as a backlog. Redeployment preserves the paused state.

`POST /api/v1/automations/{id}/rollback` with `{"revisionId":"rev_..."}` atomically reactivates a retained revision belonging to that automation and reconstructs its effective triggers. Repeating a rollback to the active revision is a no-op. The equivalent CLI is `werkt rollback AUTOMATION_ID REVISION_ID`.

## Manual runs

`POST /api/v1/automations/{id}/runs` queues the request body as the event data for a synthetic `manual` trigger. The body must be JSON and is limited to 2 MiB. Use `Idempotency-Key` to make retries safe; the same key for the same automation returns the original run with `created: false`. An optional `X-Werkt-Expected-Revision` header makes the active revision an atomic precondition; a mismatch returns `409` without queueing work, while a replay of an already-used idempotency key still returns its original run.

```bash
curl -H "Authorization: Bearer $WERKT_MANAGEMENT_TOKEN" \
  -H 'X-Werkt-Actor: agent:operator' \
  -H 'Idempotency-Key: diagnostic-2026-08-18' \
  -H 'Content-Type: application/json' \
  -d '{"reason":"diagnostic"}' \
  http://127.0.0.1:8080/api/v1/automations/process-alert/runs
```

Manual runs use the active immutable revision and the manifest's retry and concurrency policies, even while the automation is paused.

## Runs and audit history

`GET /api/v1/runs` accepts `automation`, `status`, and `limit` filters. `limit` must be from 1 to 500. `GET /api/v1/runs/{id}` returns one run.

`GET /api/v1/audit` accepts `automation` and `limit`. Deployments, pause/resume changes, newly created manual runs, secret lifecycle changes, retention plans, and retention outcomes are recorded transactionally with their database state change. Idempotent no-ops do not create duplicate audit events.
