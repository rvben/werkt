# Supervised issue investigations

The investigation registry connects a GitHub issue to its cloud conversation,
starting revision, findings, and selected patch. It does not start work on issue
arrival, estimate effort, close issues, publish fixes, or infer resolution from a
cloud task's completion. Operators explicitly select each investigation.

## Lookup before starting work

Configure `WERKT_URL` and `WERKT_INVESTIGATION_READ_TOKEN` with the server's
read-scoped token. Set `CODEX_CLOUD_CLI` to an explicitly installed and tested
Codex binary. The adapter currently requires `codex-cli 0.154.0-alpha.6.2`;
version changes require contract validation. GitHub access uses the existing
`gh` login. Never put tokens in command-line arguments or repository files.

```sh
python3 scripts/investigation.py lookup example/project 42
```

The command resolves GitHub's stable repository ID, reads every page of the
issue's registry history, and checks active registered tasks through the
supported cloud CLI. Repository renames preserve lookup. Its JSON `status` is:

- `found`: registered work exists; inspect its workflow status, findings and URL.
- `none`: a successful complete lookup found no registered investigation.
- `lookup_unavailable`: authentication, connectivity, version, schema or task
  visibility failed. Saved records may still be returned. Do not start duplicate
  work. A missing or archived cloud task is not evidence of no investigation.

The issue fingerprint covers title, body and open/closed state. Edited issues,
branch advancement, or changed cloud evidence are reported as freshness flags.
Terminal records remain readable without cloud access; their findings are
historical, not a live claim about the current branch. The registry only knows
registered investigations, not arbitrary tasks someone started elsewhere.

Add a local repository instruction requiring this lookup before issue work.
Saving a database row alone does not make another agent discover it. Test the
instruction from a fresh conversation. Do not commit private agent instructions.

## Explicit launch

Configure `WERKT_INVESTIGATION_OPERATE_TOKEN` separately for writes. This is the
existing operate scope, not a new per-repository or per-automation scope.

```sh
python3 scripts/investigation.py launch example/project 42 \
  --environment selected-environment --branch main \
  --request-id investigation-42-first --prompt-file /tmp/selected-investigation.txt
```

The prompt must describe the approved scope and contain a useful reproduction.
Issue text is untrusted input. The adapter supplies the expected base and an
instruction to stop at a tested reviewable patch. These instructions are not a
replacement for the environment's access controls.

The adapter checks existing records, commits a reservation, then commits
`submission_unknown` **before** calling `cloud exec` with one attempt. Only the
process that created the reservation may submit. Replays never submit again.
Task identity is attached only after an unambiguous response. A timeout, crash,
lost update response, or missing URL requires manual reconciliation; the cloud
CLI has no verified server-side idempotency key. Do not delete an uncertain
reservation and retry. Use the prompt's request marker to inspect possible
tasks, then attach the actual task through a versioned update. An explicitly
reconciled abandonment requires a reason and does not cancel cloud work.

Only one nonterminal investigation per repository ID/issue can exist, including
uncertain, failed, and locally owned records. `--new-generation` permits a new
reservation only when all previous records are terminal. It is an explicit
decision to revisit findings, never an automatic retry policy.

## Capture and continue locally

The supported CLI exports patches and task metadata; its `summary` is diff
counts, not written findings. Read the cloud final answer in the browser and
save a handoff containing the actual base SHA, reproduction, root cause, exact
checks and outcomes, remaining questions, and next action. Do not claim an
automatic transcript export.

```sh
python3 scripts/investigation.py capture investigation-42-first \
  --handoff-file /tmp/handoff.md --base-sha ACTUAL_FULL_COMMIT_SHA \
  --output-dir /path/to/backed-up-investigations --attempt 1
```

Capture requires an idle task and brackets the diff download with matching task
metadata. It writes unique patch, handoff and manifest files, calculates the
patch's SHA-256, then records `review`. A failed registry update leaves the
snapshot available for reconciliation. The patch is not applied automatically.
Local paths are meaningful only on the capturing machine; back them up or move
the evidence to accessible artifact storage before relying on remote readers.
The registry stores the handoff itself, but does not upload patches or ensure
artifact-reference availability.

Before taking ownership, run `python3 scripts/investigation.py claim REQUEST_ID`.
It verifies the saved patch digest and rechecks the cloud task, then uses a
versioned update from `review` to `local`, keeping the full state. This requires
the actual base, written handoff, patch reference and digest, and a `ready`
cloud observation no older than five minutes. Apply the saved patch to a clean isolated
checkout of its actual base, verify the digest, and rerun appropriate checks.

Someone can still continue the cloud conversation: Werkt cannot lock its web
composer. A changed cloud timestamp, attempt or patch invalidates local reuse;
return to `review`, reconcile and capture again. Lookup detects these changes.
Recheck before delivery; database ownership cannot prevent this external race.

`completed` requires a handoff plus explicit delivery/resolution evidence
(including already-fixed outcomes). Record whether a fix is reviewed, merged,
or released. `abandoned` requires a reason. Both retain history and prevent
further edits to that generation. Publication and issue changes still require
their own authorization.

## API and storage contract

- `GET /api/v1/investigations?repositoryId=123&issueNumber=42`: read scope;
  `{status, items, nextCursor}`, newest first. `limit` is 1–500; `cursor` follows
  the usual Werkt pagination contract. `none` refers to the returned page;
  clients must accumulate all pages. Outages return HTTP 503, never an empty
  success response. Responses are not cacheable.
- `GET /api/v1/investigations/{requestId}`: read scope; one complete record.
- `POST /api/v1/investigations`: operate scope; immutable spec. Returns 201 with
  `created: true`, or 200 with `created: false` for an exact replay. Different
  input under the same request ID or an active issue owner returns 409.
- `PUT /api/v1/investigations/{requestId}`: operate scope;
  `{expectedVersion, state}` replaces state atomically. Stale versions return
  409. Invalid transitions return 400. Task identity and actual base are
  immutable after attachment. Unknown JSON fields are rejected.

Workflow: `reserved → submission_unknown → running → review → local → completed`.
`needs_input` and `failed` keep ownership pending operator reconciliation;
`review → running` records an explicitly requested cloud continuation.
Cloud status is stored separately and never drives these transitions itself.

PostgreSQL enforces issue ownership and unique task IDs. Every accepted version
is retained with actor and time, and a compact event is written to the audit
log in the same transaction. Run and automation retention do not remove
investigation records. There is no silent record eviction or whole-registry
automation-state size limit. Include these tables in normal database backups.

Individual input bounds protect API memory and storage: handoff 64 KiB UTF-8,
reason 4 KiB, delivery evidence 8 KiB, references/URLs 2 KiB each, request ID 128
characters, branch/environment 256 bytes. Use artifact references for large
logs and patches. The JSON update envelope allows 512 KiB for escaped text.

Unattended launching, automatic browser transcript extraction, cloud
cancellation, and long-lived worker login refresh are not implemented. The
adapter uses the authenticated operator workstation, and never copies its login
into automation packages or microVMs.
