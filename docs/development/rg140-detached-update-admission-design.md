# RG-140: detached platform-update admission design (proposal)

**Status: DESIGN ONLY — NOT IMPLEMENTED / NO-GO.** Applies to the
schema-170 consolidation branch, not the production VPS. The motivating
deterministic interleaving is in
`agent/internal/tasks/platform_update_runtime_admission_race_test.go`.

## Existing invariants that must survive

1. The Agent's `BeginRuntimeMutation` persists `mutation-inflight.json`
   under `/var/lib/routegate-agent/runtime-mutations` while holding the
   exclusive `runtime.lock` flock. A crash retains the marker; there is no
   automatic force-unlock.
2. The platform-update worker is detached with `systemd-run --no-block`;
   its launcher is not the worker. Holding a lease only in the launcher
   cannot protect the updater.
3. The platform-update `update-receipts` store is a **different durable
   ledger**: `prepared → mutation_started → succeeded/failed/outcome_unknown`.
   A terminal receipt is never permission to rerun a task.
4. Reconciliation can mark an absent worker's pre-mutation receipt failed
   or a started receipt outcome-unknown; it does not rerun the updater.
5. The update can replace/restart Agent binaries or services. A lock tied
   only to the old Agent process cannot be the sole lifetime guarantee.

## Required atomic handoff

Use the **existing runtime-mutation lock namespace**, not a second lock.

- Before the detached worker can cross `mutation_started`, it must
  atomically exclude ordinary Agent mutations and persist an **update
  admission marker** in that same protected directory. The marker must
  identify the exact update task and its receipt, not just a generic busy
  boolean. Its ownership and crash state must be verifiable.
- Ordinary `BeginRuntimeMutation` must refuse admission whenever an
  unresolved update marker exists, even after reboot or Agent restart.
  This check must occur **while holding the same lock**, before writing
  an ordinary mutation marker.
- The worker must also reject an existing ordinary marker, including
  malformed/unreadable state. The update marker must be durable *before*
  any updater command executes, and must remain durable if the worker dies.
- The short critical section can end after a durable reservation is
  established; do **not** rely on a process-local flock spanning an
  updater that may replace/restart its owner. Only an authenticated,
  independently verified terminal reconciliation may clear the marker.
- Preserve the update receipt as the authoritative source for
  **same-task at-most-once** semantics. The cross-protocol marker is only
  an exclusion fence, **not** a second receipt state machine.
- Define recovery authorization, independent runtime evidence, and
  marker-clearing order before writing the implementation. Unknown
  outcome must remain blocked rather than being treated as success.

## Deterministic tests required before enabling

| Interleaving / crash cut | Expected behavior |
| --- | --- |
| Worker observes no state; Agent admits mutation first | Worker cannot reserve update or start updater |
| Agent observes no state; worker reserves first | Agent cannot begin config/service mutation |
| Worker reserves; process dies before updater starts | Durable reservation survives; no automatic redispatch |
| Worker starts updater; Agent restarts | Reservation still blocks all mutating Agent jobs |
| Updater restarts Agent or replaces executable | Reservation remains valid independently of old process |
| Duplicate update task / duplicate worker | Existing receipt and reservation reject second mutation |
| Receipt succeeds but acknowledgement fails | No second mutation; reconciliation and verified release required |
| Receipt outcome unknown or corrupt marker | Fail closed; no forced clearing |
| Reconciliation races with a worker launch | Cannot clear a live or ambiguous reservation |
| Old Agent or flag-off legacy path | Compatibility decision explicit; never silently bypass reservation |

## Implementation sequencing

1. First build a pure, injectable admission primitive and deterministic
   two-party tests. Keep `BeginRuntimeMutation` as the sole owner of
   ordinary mutation admission; reuse its lock helper, don't fork it.
2. Extend detached worker admission to the **same** primitive; verify
   marker durability, atomicity and permissions. Do not turn on
   dispatch while the worker and Agent disagree on the protocol.
3. Add crash/restart and receipt-reconciliation tests with injected
   filesystem/worker evidence; then run Linux integration tests.
4. Review migration/upgrade compatibility for older deployed Agents,
   including fail-closed behavior when a new marker exists.
5. Require exact-HEAD Agent and full CI, independent review, and a
   separate release decision. No production changes from this proposal.

**Important:** `RuntimeMutationStatePresent` is an observation, not
admission. The existing interleaving test proves that distinction; it
does not test a fixed handoff or establish release readiness.
