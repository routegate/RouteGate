# ADR-0033 — Transaction-bound Manager claim rehearsal before any real dispatcher cutover

**Status:** RG-140 Draft; *test-only* SQL rehearsal in disposable PostgreSQL. Not an operational claim implementation. No schema migration.

## Why an isolated transaction rehearsal

ADR-0030 introduced a Manager-origin, immutable pre-dispatch checkpoint and an audit event. ADR-0031 introduced a preparation-only Agent history with exactly `prepared_unattested`. ADR-0032 confirmed that historical Manager and Agent-side reads can never themselves authorize dispatch or recovery.

Today the **live** Agent polling paths independently claim:
- `config_apply_jobs` in `ClaimNextConfigTask`; this legacy query may assign an Agent when `agent_id IS NULL`.
- `agent_operation_jobs` in `ClaimNextAgentOperationTask` with an eligible `vpn_core_service` operation.

Neither live query consults the dormant Manager checkpoint. Also, the existing checkpoint creation requires an **already bound Agent** before the job has been claimed, so it cannot be simply inserted after the legacy claim. Opt-in onboarding will need to establish binding before checkpoint creation, without altering legacy Agent behavior.

## This Draft's test-only transaction

The new `backend/internal/db/runtime_checkpointed_claim_rehearsal_*_test.go` files compile into the **test binary only**, not the Manager. They execute two separate versions of the same conditional claim using the exact existing `config_apply_jobs` and `agent_operation_jobs` tables.

Inside one PostgreSQL transaction, the rehearsal checks:

1. The job is **pending**, unstarted, explicitly Agent-bound and of the correct action/kind.
2. A current Agent with the exact token hash, compatible fencing capability, same server and current credential generation exists.
3. An immutable Manager-origin checkpoint matches the specific job, Agent, server, generation and (for config apply) the Manager-stored version/digest. Checkpoint phase is `pre_dispatch_unattested` and epoch positive.
4. The checkpoint's `committed` event and preparation's `prepared_unattested` event each exist exactly once in their respective immutable audit logs.
5. The historical preparation's generation matches **current** Manager Agent generation. The preparation itself remains **unattested**, and is not an actual enrollment.
6. No post-event recovery reservation or active platform update is present in the snapshot.
7. The selected job is locked `FOR UPDATE`; Agent, config, checkpoint and preparation are shared-locked; only then can the test transition to `in_progress` in that same transaction.

A simulated failure after updating the job but before committing leaves it pending. Two competing claims allow one winner; a lost network response cannot cause a second claim of the same job. Invalid bearer, missing checkpoint, missing preparation, capability loss and credential-generation drift refuse without mutating the job.

These tests exercise real PostgreSQL lock and rollback semantics. They are **not** a live cutover and do not execute a VPN task.

## Crucial blockers before any production dispatcher integration

- **No genuine enrollment:** A preparation record is not proof of an enrolled host. No production dispatcher must enable this rehearsal based on `prepared_unattested` alone. A separately authenticated operator opt-in, durable Agent enrollment certificate/genesis and explicit state transitions are necessary.
- **Ordering of legacy jobs:** Existing `ClaimNextConfigTask` permits null Agent ownership. The protected route must use an Agent-bound job and a committed checkpoint *before* claiming it. Legacy jobs retain the old route unless explicitly migrated.
- **All mutation paths:** A task-only claim gate cannot secure adjacent account transfers, routing updates, credential removals, platform upgrades, installer state or running sessions. Each route requires independent threat-model coverage; default-on gating of just these two claims would create a false sense of protection.
- **Transaction vs network:** PostgreSQL COMMIT proves only a Manager-side state transition, not Agent receipt, local `fsync`, process identity, active connections or successful traffic. A committed claim followed by lost HTTP response must remain in reconciliation, not be re-dispatched.
- **Lock order and adversaries:** Test-only row locking cannot substitute for the Agent's runtime-mutation flock, trusted journal genesis, host provenance, Manager authz scope or an independent operator review. Avoid deadlocks against existing registration/Agent rotation and credential-revocation transactions.
- **Manual clearance only:** Neither a matched hash, historical preparation, successful claim nor local journal can authorize removing the runtime mutation fence. Hysteria2 as well as VLESS require explicit acceptance before release.

## Acceptance

Run dedicated `go test -race` against disposable PostgreSQL for both job kinds, with negative stale/missing evidence, injected pre-commit abort and concurrent two-client claims. Run full RouteGate CI (all six jobs) and Workspace Integration on the exact HEAD.

Keep Draft and **do not** edit `ClaimNextConfigTask`, `ClaimNextAgentOperationTask`, Agent Runner, installer, `main`, or any production VPS/database. The next work item is a separately reviewed per-Agent opt-in enrollment and real dispatcher design, not a blind swap of these test queries.
