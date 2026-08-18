# Management API

The management API is the stable control-plane surface for operators, external agents, and a future UI. It is distinct from trigger ingress: knowing a webhook URL does not grant access to inventory, run history, lifecycle changes, or manual execution.

The machine-readable OpenAPI 3.1 contract is served at `GET /api/openapi.yaml` and does not require authentication.

## Authentication and attribution

Set `WERKT_MANAGEMENT_TOKEN` and send it as a bearer token on every management request:

```http
Authorization: Bearer <token>
X-Werkt-Actor: agent:operator
```

`X-Werkt-Actor` is recorded in the audit trail but is attribution supplied by the authenticated caller, not a separate identity proof. If omitted, it is recorded as `api`. When `WERKT_MANAGEMENT_TOKEN` is empty, management authentication is disabled for local development and Werkt emits a startup warning. Werkt binds to `127.0.0.1:8080` by default; explicitly configure both a token and `WERKT_LISTEN_ADDR` before exposing it beyond the host.

Health checks and trigger ingress (`/hooks/...` and `/email/...`) do not accept the management token as authority and remain outside this middleware.

## Automations

`GET /api/v1/automations` returns the organized inventory. Optional filters are `project`, `folder`, `label`, `q`, and `enabled`.

`GET /api/v1/automations/{id}` returns metadata, the active manifest, effective trigger state, and revision history.

`PATCH /api/v1/automations/{id}` accepts one strict JSON shape:

```json
{"enabled": false}
```

Pausing blocks schedule, webhook, email, and ntfy ingestion. Manual runs remain available for diagnosis. Resuming recalculates each schedule from the next future occurrence, so missed intervals are not replayed as a backlog. Redeployment preserves the paused state.

## Manual runs

`POST /api/v1/automations/{id}/runs` queues the request body as the event data for a synthetic `manual` trigger. The body must be JSON and is limited to 2 MiB. Use `Idempotency-Key` to make retries safe; the same key for the same automation returns the original run with `created: false`.

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

`GET /api/v1/audit` accepts `automation` and `limit`. Deployments, pause/resume changes, and newly created manual runs are recorded transactionally with the state change. Idempotent no-ops do not create duplicate audit events.
