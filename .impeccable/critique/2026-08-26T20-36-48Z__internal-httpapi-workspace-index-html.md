---
target: the Werkt workspace from human and agent perspectives
total_score: 29
max_score: 40
na_heuristics:
p0_count: 1
p1_count: 3
timestamp: 2026-08-26T20-36-48Z
slug: internal-httpapi-workspace-index-html
---
# Werkt workspace critique

## Design Health Score

| # | Heuristic | Score | Key Issue |
|---|-----------|------:|-----------|
| 1 | Visibility of System Status | 3 | Lifecycle and connection state are strong; environment and acting identity are absent before mutations. |
| 2 | Match System / Real World | 3 | Excellent operator language, but cron, trust, and concurrency concepts assume domain fluency. |
| 3 | User Control and Freedom | 3 | Back, close, cancel, and safe dismissals are strong; completed lifecycle changes have no undo. |
| 4 | Consistency and Standards | 2 | A terminal deployment step can read both “Succeeded” and “Not started.” |
| 5 | Error Prevention | 3 | Revision pinning and confirmations are excellent; wrong-environment action remains insufficiently guarded. |
| 6 | Recognition Rather Than Recall | 3 | Core actions and identity are visible; generic recovery guidance and truncation still create recall burden. |
| 7 | Flexibility and Efficiency | 3 | Keyboard support is strong; global history lacks search, sorting, cursors, and batch workflows. |
| 8 | Aesthetic and Minimalist Design | 3 | Calm and purposeful; the automation inventory consumes space in unrelated global history views. |
| 9 | Error Recovery | 3 | Exact errors, logs, bundles, and reruns are first-class; the next safe action is not. |
| 10 | Help and Documentation | 3 | Help is concise, but “Recovery guide” opens a broad keyboard/operator guide. |
| **Total** | | **29/40** | **Good — strong foundation, trust-critical rough spots remain.** |

## Design Specificity Verdict

**Human assessment:** Product-specific structure, disciplined but not yet unmistakable visual signature. The inventory/detail/diagnosis topology, immutable revision identity, artifact trust, and pinned-revision intervention copy feel authored for Werkt. The warm white, hairline, system-type visual language is high quality but transferable; the diagnosis pane and precise copy carry most of the character.

**Deterministic scan:** Five advisory findings in `workspace.css`: two undocumented radii (`1rem` at line 283 and `2rem` at line 314) and three undocumented colors (lines 362, 503, and 559). The radii are likely intentional false positives for an underline and fully rounded chips. The colors are plausible skeleton/scrim exceptions but should become explicit tokens. The detector used a degraded regex fallback because its parser packages were unavailable, so computed selector and contrast checks were not performed.

**Browser evidence:** Separate desktop (`1440×900`) and mobile (`390×844`) inspections showed no document-level horizontal overflow and no console warnings/errors. Mutable detector injection was blocked by browser security policy, so no reliable user-visible overlay exists.

## Overall Impression

Werkt already feels like a serious operational instrument. It is calm, exact, keyboard-friendly, and far better at exposing failure truth than most control planes. The biggest opportunity is to make trust as explicit around actions as it already is around diagnostics: where am I, who am I, what exactly happened, and what is the one safest next step?

## What’s Working

- **Failure diagnosis is the signature experience.** Exact errors, attempt counts, logs, structured output, event/revision IDs, and a copyable diagnostic bundle turn panic into concrete understanding.
- **Intervention semantics are exceptionally clear.** Pause and manual-run dialogs explain ingress, queued/running work, missed schedules, idempotency, and the pinned revision before action.
- **The interface adapts instead of merely shrinking.** Tables become labeled rows, bottom navigation persists, diagnosis becomes focused on mobile, status never relies on color alone, and keyboard/focus contracts are unusually deliberate.

## Priority Issues

### 1. [P0] The served OpenAPI contract advertises a fixed localhost server

- **What:** `openapi.yaml` declares `http://127.0.0.1:8080` as its server even when discovered from another origin.
- **Why it matters:** A generated or autonomous client can inspect the right contract and then send mutations to the wrong process. That is an agent-safety failure, not a documentation nit.
- **Fix:** Omit `servers` or use a relative origin; add a contract test that resolves the served spec from a non-default port.
- **Suggested command:** `$impeccable harden`

### 2. [P1] Operators cannot verify scope or identity before intervention

- **What:** The persistent header says only “Connected.” Pause, Run now, Retry, and rollback do not show environment, server/instance, acting identity, or privilege.
- **Why it matters:** The easiest catastrophic operator mistake is a correct action in the wrong environment or under the wrong identity.
- **Fix:** Add a compact persistent scope label such as `Production · werkt.example · operator:console`; repeat environment and actor in high-impact confirmations.
- **Suggested command:** `$impeccable harden`

### 3. [P1] Deployment status can contradict deployment timing

- **What:** `stepDuration()` returns “Not started” whenever `startedAt` is absent, even if the step status is `succeeded` or `failed`.
- **Why it matters:** A control plane loses authority when a promotion timeline says “Succeeded · Not started.” Operators cannot distinguish missing telemetry from work that never ran.
- **Fix:** Reserve “Not started” for queued/pending steps. For terminal steps with missing timestamps, show “Time unavailable” or an em dash and flag the data-quality mismatch.
- **Suggested command:** `$impeccable clarify`

### 4. [P1] The agent history contract is expensive and not machine-actionable enough

- **What:** Run lists return full `Run` objects, including logs and arbitrary results; errors are only `{error: string}`; runs/audit are bounded without cursor pagination.
- **Why it matters:** Agents consume unnecessary bandwidth and tokens, must parse prose to distinguish conflicts, and cannot prove an audit is complete beyond the bounded window.
- **Fix:** Introduce `RunSummary` for list endpoints; use typed problem details with stable `code`, `retryable`, `requestId`, and conflict context; add cursor pagination and richer audit filters.
- **Suggested command:** `$impeccable harden`

### 5. [P2] Recovery presents tools, not a recommended path

- **What:** A failed run immediately presents seven choices across tabs and recovery actions. “Recovery guide” opens the generic “Operate from the keyboard” dialog rather than contextual guidance for the observed 422/503 failure.
- **Why it matters:** The emotional peak is excellent diagnosis; the ending then makes a stressed operator decide among tools without a clear safest next action.
- **Fix:** Name one recommended next step from current state and error type. Keep logs visible, but demote copy/rerun/general help into secondary disclosure and open contextual recovery content.
- **Suggested command:** `$impeccable distill`

## Persona Red Flags

**Alex — power user:** Keyboard shortcuts are excellent, but Runs, Deployments, and Audit lack search, sorting, cursors, and batch workflows. The persistent Automations inventory consumes global-history width without governing those tables. Long durations render as `675m 40s`, which is exact but slow to scan.

**Sam — accessibility-dependent user:** Semantics, focus, live regions, labels, and words-plus-color are strong. The “Recovery guide” semantic mismatch is confusing when announced, dense metadata relies heavily on 10–11px type, and exact IDs need explicit 200% zoom verification.

**Casey — distracted mobile operator:** Bottom navigation and full-screen diagnosis work well. However, switching global views after closing diagnosis can preserve an irrelevant scroll offset, the back control sits outside the comfortable thumb zone, and seven recovery choices are too many under stress.

**Morgan — on-call operator:** “Connected” is insufficient proof of environment, instance, identity, and authority. Contradictory deployment timing is a stop-the-line trust problem. “Unattested” is shown accurately but has no adjacent explanation of whether to block, investigate, or accept execution.

**External coding agent:** Deployment idempotency, expected revision, provenance, and transactional semantics are excellent. The hard-coded OpenAPI origin, prose-only conflicts, full diagnostic list payloads, missing cursor pagination, and unscoped bearer authority make safe autonomous operation harder than the core design deserves.

## Agent/API Parity Gaps

- Initial deployment, secrets, and retention are API/CLI-only, while the workspace describes itself as the shared management surface.
- Audit event IDs and `details` are discarded in the UI; exact events cannot be inspected or deep-linked.
- Selected deployments poll; selected running runs do not, so open run truth can go stale.
- Run and deployment diagnosis are not represented in the URL, preventing exact incident deep links.
- One bearer token spans read, operate, deploy, secrets, and destructive retention work; `X-Werkt-Actor` is attribution, not proof of identity.

## Cognitive Load and Emotional Journey

**Moderate load: 3/8 checklist failures.** Chunking, one-thing-at-a-time, and minimal choices fail in dense diagnosis/help states. Decision points above four options include seven run-diagnosis choices, five run-status filters, and nine shortcut rows.

The entry is calm and confidence-building; exact failure diagnosis is the emotional peak. The valleys are trust contradictions, missing environment/identity before action, and generic recovery after a highly specific diagnosis.

## Minor Observations

- Mobile global-view navigation can preserve an irrelevant scroll offset instead of returning to the title/filter.
- “1 most recent immutable revisions” needs singular grammar.
- Global Audit is visually sparse while retaining the full inventory pane.
- Run lists expose diagnostics the table never renders.
- The design scan’s undocumented skeleton/scrim colors should become intentional tokens; the two radius advisories are likely false positives.
- `.impeccable/design.json` is stale relative to `DESIGN.md`, reducing detector confidence in tonal-ramp checks.

## Questions to Consider

- What must be permanently visible before every intervention: environment, actor, revision, or all three?
- Is “Recovery guide” documentation, a decision assistant, or the next recommended action?
- Should a global history screen retain a selected automation at all?
- If Werkt is a bench instrument, what is its equivalent of an engraved range/scope label?
