# RG-140: detached platform-update admission and recovery

**Status: PARTIALLY IMPLEMENTED — Draft / NO-GO.** Applies to the
schema-170 consolidation branch only; no production VPS changes.

## Current implementation and evidence

- Detached update worker acquires the existing `BeginRuntimeMutation`
  lease before `MarkMutationStarted`, and holds it through updater
  execution. Successful updater + durable success receipt precede
  `lease.Complete()`. A crash or uncertain result retains the marker.
- The legacy Agent path now uses that same lease for config apply,
  VPN-core service/install and maintenance, even with experimental fencing
  disabled. Cancellation **before** the handler runs clears only its own
  unchanged marker; errors after entering the handler retain uncertainty.
- Update dispatch/worker preflight uses
  `RuntimeMutationIdleForPlatformUpdate` instead of treating a completed
  lease's directory as permanently busy. The actual worker still performs
  atomic admission under the shared lock; preflight is not a reservation.
- Existing test files cover mutual exclusion in both directions, process
  exit, damaged marker, post-completion admission, legacy handler error,
  and cancellation before handler execution. They do **not** exercise a
  real systemd transient unit or a live upgrade/restart.
- **Unresolved:** a crash after the success receipt but before clearing
  the shared marker leaves a safe-but-stuck admission. No authenticated,
  independently verified clearance protocol exists yet.
- **Unresolved:** legacy Agent handler errors and lost Manager
  acknowledgements may leave a durable marker without an automated
  recovery path. Compatibility and result/reconciliation semantics need
  independent review.
- **Unresolved:** old deployed Agent versions that do not use this
  shared lease may still mutate during an update. Upgrade sequencing and
  feature negotiation require explicit compatibility controls.
- **Unresolved:** the detached worker's process lifetime, Agent binary
  replacement/restart and reconciliation races need real systemd tests.
  Exact-HEAD CI has not passed yet. **Do not release or deploy.**

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

## Recovery authorization: required decision matrix (not implemented)

A terminal receipt is **not** an authorization to clear
`mutation-inflight.json`. Recovery must serialize against new admissions
using the existing `runtime.lock` and must not add a second lock namespace.

| Receipt / runtime evidence | Recovery decision |
| --- | --- |
| `prepared`, no worker, no mutation intent | Reconcile as pre-dispatch failure; never rerun the same task |
| `mutation_started`, worker still active | Do not clear; continue observing |
| `mutation_started`, worker absent | Mark `outcome_unknown`; retain the marker |
| `succeeded`, marker already absent | No recovery needed; no update replay |
| `succeeded`, marker still present | Require independently verified installed version/runtime health and worker absence; do **not** auto-clear from receipt alone |
| `failed` or `outcome_unknown` after mutation started | Retain marker pending independent diagnosis and explicitly authorized recovery |
| Missing, corrupt, mismatched or unsafe marker/receipt | Fail closed; never infer ownership from task ID alone |
| Probe error, unit still activating, or evidence changing during checks | Fail closed; retry observation, not mutation |

**Proposed clearance protocol, subject to security review:**

1. Obtain an explicitly authorized recovery request bound to the exact
   task UUID, expected version and node identity. Never accept a generic
   unlock command or trust the task ID alone.
2. Under the **existing** exclusive admission lock, validate the owned
   marker, matching durable receipt, filesystem permissions and the
   fixed task-specific systemd unit identity. Reject unknown files.
3. Independently establish that the updater is no longer running and
   verify the installed release/runtime state, including post-restart
   service health. A successful receipt by itself is insufficient.
4. Recheck evidence under the lock immediately before clearance. Only
   remove the exact verified marker with durable directory sync; do not
   delete unrelated evidence or reopen a failed/unknown update task.
5. Audit the authorization, evidence and outcome without exposing
   credentials. On any uncertainty or I/O failure, preserve the fence.

The current `RuntimeMutation.Complete` intentionally requires a live
lease owner and is **not** a recovery API. Do not retrofit it into a
force-unlock path. Build injectable verification and crash-cut tests
before introducing any production recovery operation.

## Upgrade compatibility gate (not implemented)

An older Agent that never calls `BeginRuntimeMutation` cannot be
excluded by the new worker's flock. Manager must not dispatch a detached
update solely because a node reports general Agent connectivity.
Require explicit compatible Agent capability/version negotiation and
verify it survives restarts before enabling updates. Mixed-version
deployments need a documented rollout and rollback procedure; no
automatic bypass for legacy nodes.

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
