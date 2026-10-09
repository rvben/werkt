# Supervised cloud investigations

This dispatcher retains Werkt's existing investigations workflow. It starts one
cloud task at a time for eligible high-urgency issues, persists intent before
submission, and observes cloud status without automatically resubmitting an
ambiguous or previously registered investigation. Classification events drive
normal dispatch; six-hour reconciliation recovers missed events.

A ready cloud task becomes `needs_input`, with its conversation link retained.
It does not create an issue review, approve a patch, notify a Mac runner, apply
changes, or call a factory API. An operator can explicitly capture a stable
snapshot and perform local takeover using `scripts/investigation.py`; see
[the investigation workflow](../../docs/investigations.md).

The two-model factory, review UI, Pushover decisions and isolated local checks
belong to the separate Fabriek product.

## Existing installations

Script replacement and service restarts require operator deployment approval.
Back up the installed scripts and persistent state before an upgrade. Install
`worker.py`, `listener.py` and `scripts/investigation.py` together. Preserve the
existing service, credential files, repository/environment mapping, dispatcher
role, outbox triggers and schema-1 `dispatch.json`. The cloud CLI contract remains
pinned to the worker's validated version.

The legacy `review-queue.json` filename is retained for compatibility: it tracks
cloud conversations needing observation, rather than factory approvals. Terminal
conversations and operator-owned review/local snapshots leave that queue. Identical
observations do not rewrite registry versions or race explicit capture. No Mac
review runner or issue-review notification route should be installed for this
workflow. Existing installations with those additions need a separate reviewed
removal; changing this checkout does not change a running service or its state.

Run adapter and dispatcher tests before deployment:

```sh
python3 -m unittest discover -s scripts -p 'test_investigation.py' -v
PYTHONPATH=scripts python3 -m unittest discover -s deploy/investigator -v
```
