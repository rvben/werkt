---
target: this app
total_score: 24
max_score: 40
na_heuristics: 
p0_count: 0
p1_count: 3
timestamp: 2026-08-23T19-43-32Z
slug: internal-httpapi-workspace-index-html
---
Method: dual-agent (A: `/root/design_review` · B: `/root/detector_browser`)

## Design Health Score

| # | Heuristic | Score | Key issue |
|---|---|---:|---|
| 1 | Visibility of System Status | 2 | Strong loading, connection, polling, badges, and toast feedback, but request races and all-or-nothing loading can show stale or incomplete operational truth. |
| 2 | Match System / Real World | 3 | Operator language is precise; terms such as `forbid`, idempotency, HMAC, and RFC 5322 receive little inline interpretation. |
| 3 | User Control and Freedom | 3 | Dialog exits, Escape behavior, focus return, and safe confirmation focus are strong; views and filters are not durable or shareable, and pause has no undo. |
| 4 | Consistency and Standards | 3 | The component and interaction language is cohesive; mobile connection controls and several ARIA state patterns diverge. |
| 5 | Error Prevention | 2 | JSON validation, idempotency defaults, disabled mutation controls, and rollback/cancel confirmations are good, but Pause is immediate and overlapping detail requests are not guarded. |
| 6 | Recognition Rather Than Recall | 3 | Breadcrumbs, exact IDs, visible nav labels, and contextual diagnosis reduce memory work; mobile hides connection meaning and filters are not encoded in the URL. |
| 7 | Flexibility and Efficiency | 2 | Search shortcut, filters, direct tables, and keyboard-operated tabs help; no fast failure workflow, list navigation, persistent views, or batch inspection exists. |
| 8 | Aesthetic and Minimalist Design | 3 | Calm, dense, flat, and purposeful; duplicate run-row targets and weak exception emphasis keep it from excellent. |
| 9 | Error Recognition and Recovery | 2 | Errors are specific and user input is preserved, but failed-run diagnosis buries the error and offers no next safe recovery action. |
| 10 | Help and Documentation | 1 | API v1 and explanatory copy exist, but task-focused contextual help is sparse. |
| **Total** |  | **24/40** | **Acceptable — strong authored foundation, significant operational UX gaps.** |

## Design Specificity Verdict

**LLM assessment:** Strongly authored for Werkt. The persistent navigation rail, grouped inventory, exact automation detail, and contextual diagnosis pane express the product far better than a generic KPI dashboard or node canvas would. Monospace is reserved for machine identity, the paper-and-hairline system feels like a bench instrument, and rollback copy is unusually precise. The missed opportunity is exception management: failures are understated at the moment Werkt should feel most decisive.

**Deterministic scan:** The CLI detector returned zero findings for `internal/httpapi/workspace/index.html`, but it ran in degraded regex mode because `htmlparser2`, `css-select`, `css-tree`, and `domutils` were unavailable. Custom properties, selector matching, and computed contrast were not evaluated, so the result is an undercount rather than a clean bill of health.

**Visual evidence:** The independent design assessment exercised populated desktop and mobile views through a temporary mock API, including automation detail, failed-run diagnosis, manual-run, and rollback flows. A second browser pass confirmed the production shell and designed `Workspace unavailable` state. Mutable script injection was unavailable in the browser API, so no reliable user-visible `[Human]` overlay exists.

## Overall Impression

Werkt already has the right visual world: direct, disciplined, and product-specific. The biggest opportunity is to make the interface as trustworthy and useful under failure as it looks at rest. The current risks are not decorative; they concern stale data, silently truncated history, weak exception hierarchy, and incomplete mobile semantics.

## What's Working

- The four-region desktop composition preserves global orientation, inventory, active work, and diagnosis without becoming a generic dashboard.
- Exact revisions, run IDs, event IDs, attempts, logs, and outputs remain visible. Risky rollback confirmation clearly explains scope, trigger replacement, and pinned-running-job behavior.
- Security and implementation hygiene are solid: dynamic API values are escaped before HTML insertion, the management token stays in `sessionStorage`, and workspace assets receive a restrictive CSP plus COOP, referrer, and MIME-sniffing headers. The Go test suite and `go vet ./...` pass.

## Priority Issues

### 1. [P1] The client can display stale or incomplete system truth

**Why it matters:** `selectAutomation()` starts parallel detail/run/audit requests without cancellation or a request-generation guard. A slow response for automation A can overwrite a later selection of B. Global runs, deployments, and audit also silently stop at 100 records while their headings imply complete history. Both behaviors violate the product's “show system truth” principle.

**Fix:** Add an `AbortController` or monotonically increasing request token and commit results only when the response still matches the current automation/view. Add cursor pagination to history endpoints, or at minimum label the UI “Latest 100” and expose loading for older records. Make route/view state explicit enough that the rendered detail can be asserted against the selected ID.

**Suggested command:** `$impeccable harden`

### 2. [P1] Exception hierarchy stops before recovery

**Why it matters:** A failed inventory item communicates failure visibly only through a red dot; its visible secondary line remains the ordinary description. Items remain alphabetically sorted, and the diagnosis Summary places the exact error as the last ordinary fact with logs behind another tab. The operator can gather evidence but receives no clear next safe action.

**Fix:** Add a visible health line or badge such as `Latest run failed`, `Running`, `No runs yet`, or `Paused`; add a failure count/filter and optionally sort exceptions first without destroying project/folder grouping. Lead failed diagnosis with a failure block containing the exact error, attempt exhaustion, and actions such as `Open logs`, `Copy diagnostic bundle`, and a clearly scoped rerun when allowed.

**Suggested command:** `$impeccable shape` followed by `$impeccable layout`

### 3. [P1] Mobile and dynamic-state accessibility is incomplete

**Why it matters:** At 42rem and below, the visible and accessible text inside the connection state and Connection button is hidden, leaving only aria-hidden visuals. Filter chips update only a CSS class, not `aria-pressed`. The full-screen mobile diagnosis pane is not a dialog or focus-contained surface, diagnosis tabs lack `aria-controls`/`tabpanel` relationships, and replacing the entire live inventory on every keystroke can be noisy for screen readers.

**Fix:** Give Connection a permanent accessible name and retain an `sr-only` live status that responsive CSS never hides. Synchronize filter `aria-pressed`, add complete tab/panel relationships, and treat full-screen diagnosis as a focus-contained dialog-like surface while keeping the desktop pane nonmodal. Announce result counts rather than the whole replaced inventory.

**Suggested command:** `$impeccable audit` followed by `$impeccable adapt`

### 4. [P2] Loading is fragile and the browser layer is untested

**Why it matters:** The initial workspace load joins automations, runs, deployments, and audit with `Promise.all`, then joins detail, runs, and audit again. One secondary-feed failure replaces the whole workspace with a fatal error. The per-automation audit response is fetched but never rendered. Existing tests verify embedded assets and security headers, but no test exercises client rendering, request races, auth recovery, dialogs, keyboard behavior, or responsive semantics.

**Fix:** Render automations as the critical path and let history panes fail and retry independently. Remove the unused detail-audit fetch or expose it intentionally. Add browser-level tests with mocked APIs for slow out-of-order selection, partial endpoint failure, 401 reconnection, empty/long data, destructive confirmations, keyboard tabs, and mobile focus behavior.

**Suggested command:** `$impeccable optimize` followed by `$impeccable harden`

### 5. [P2] Operator state and high-impact actions need more durable context

**Why it matters:** View, search, and filters are lost on refresh and cannot be shared; only automation selection reaches the URL. Pause is immediate even though it stops schedule and ingress triggers, while confirmation quality is otherwise high. Mutation outcomes disappear with a short toast, which is weak as the final reassurance for an operational action.

**Fix:** Put view/filter/search state in the URL, add arrow-key inventory navigation and shortcuts for view/refresh/diagnosis, and preserve the operator's exception-focused workspace. Confirm Pause with one compact sentence explaining which triggers stop, that running jobs continue, and that manual runs remain possible. Pair transient toasts with a durable updated-state highlight or action receipt.

**Suggested command:** `$impeccable clarify` followed by `$impeccable polish`

## Cognitive Load

Moderate: 3 of 8 checklist items fail.

- **Chunking:** run diagnosis presents eight facts as one list; deployment diagnosis can combine failure, steps/logs, and many identity/timestamp facts.
- **Visual hierarchy:** the highest-value failure detail is styled like the least important fact.
- **Minimal choices:** native status selectors expose five run choices and nine deployment choices. Group deployment states into `In progress`, `Succeeded`, and `Failed/cancelled`, then offer exact states as a secondary refinement.

Single focus, grouping, one-thing-at-a-time, working-memory support, and progressive disclosure generally pass. The diagnosis pane is particularly effective at avoiding context switches.

## Persona Red Flags

**Alex, power operator:** `Cmd/Ctrl-K` is a good start, but repeatedly finding the next failure requires generic Tab navigation and reconstruction of filters after reload. There is no shareable failure view, arrow-key inventory navigation, or one-keystroke “next failed automation” path.

**Sam, keyboard/screen-reader operator:** Desktop focus styles, skip link, native dialogs, safe confirmation focus, keyboard tabs, and diagnosis return-focus are strong. Exact failures are the unnamed mobile Connection button, hidden mobile live status, class-only filter selection, incomplete tab relationships, and potentially noisy full-list live-region replacement.

**Morgan, on-call Werkt operator:** Morgan scans for exceptions, opens a failed run, and needs the next safe intervention. The inventory offers only a red dot, a request race can show the wrong detail after quick selection, the exact error is buried, and no retry or diagnostic-bundle action completes the recovery path.

## Minor Observations

- Each run row has both a clickable run ID and a chevron for the same destination; use one clear row-level action.
- Relative times become stale while a page remains open unless another render occurs.
- The manual-run dialog should show the exact target revision, not only say “active immutable revision.”
- The API exposes secret lifecycle and retention plan/apply workflows that the workspace does not cover. Treat these as an explicit product-scope decision; if operators must have the same facts and actions as agents, add them with the same careful safety model rather than quietly leaving them CLI-only.
- Empty states are specific and useful, especially the CLI guidance.

## Questions to Consider

- If “manage by exception” is a design principle, why is failure less visible than the automation description?
- Should diagnosis remain a passive record viewer, or become the place where an operator confidently completes recovery?
- What would a one-keystroke “next failed automation” workflow look like?
- Should the workspace deliberately stay a subset of the API, or eventually include secrets and retention so operator and agent capabilities converge?
