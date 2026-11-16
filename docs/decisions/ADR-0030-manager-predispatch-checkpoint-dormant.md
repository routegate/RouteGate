# ADR-0030 — Dormant Manager-owned pre-dispatch recovery checkpoint

**Status:** RG-140 Draft / schema and isolated PostgreSQL tests only. No dispatch integration, enrollment, recovery, or unlock.

## Context

RG-140's existing `vpn_runtime_recovery_reservations` and `vpn_runtime_recovery_attempts` are post-event records and contain hashes that may originally have been reported by an Agent bearer holder. They cannot serve as an independent pre-mutation history. ADR-0029 added a *pure* Manager-side reconstruction of the expected Agent mutation-intent marker, but it did not commit that result to Manager storage.

A future crash-safe protocol needs an immutable Manager-origin checkpoint **before a task can leave Manager**. PostgreSQL row locks and an audit event can safely stage this requirement, but not prove by themselves that the original Agent process received the task or wrote its journal.

## What migration 000166 actually creates

Two new, initially empty tables:

- `vpn_runtime_pre_dispatch_checkpoints`: one immutable snapshot for an eligible, **pending**, not-yet-started `config_apply` or `vpn_core_service/restart` job. Fields include Manager job ID/type, Agent and server IDs, credential generation, an independent exact-byte marker SHA-256, optional config version and its Manager-stored config hash, and a monotonically generated checkpoint epoch.
- `vpn_runtime_pre_dispatch_checkpoint_events`: one mandatory `committed` audit entry created in the same PostgreSQL transaction.

The caller can supply **only** `task_id` and `task_kind`. The insert guard acquires `FOR UPDATE` on the existing Manager job and shared locks on the matching Agent and (for config apply) version row. It requires a current, non-disabled Agent with the existing `runtimeMutationFencingV1` capability, a positive key generation, a still-pending eligible job and matching node ownership. The trigger independently calculates the Agent v1 marker digest from Manager-owned job bytes; it rejects caller-supplied digests/IDs/generation and an existing post-event recovery reservation.

The snapshot and audit **either commit together or do not exist**. Updates, deletes and truncations of either table are refused. The source job's identity and version's hash/rendered configuration cannot be rewritten after a checkpoint exists, but ordinary job status changes remain possible. Unique constraints refuse replay and, intentionally for this stage, allow at most one experimental checkpoint per node in the absence of a separately reviewed terminal/re-enrollment protocol.

The down migration rejects deletion of ANY retained checkpoint or audit record, preventing loss of history during downgrade.

## Exact limits (security-critical)

**This does not yet guarantee that any Agent task was preceded by a checkpoint.** There is NO application writer, no dispatcher dependency, no endpoint, no automatic enrollment and no Agent-side use of the record. Existing jobs continue to follow their existing lifecycle. The repository must not claim a general pre-dispatch guarantee before the dispatcher is intentionally updated and the migration/enrollment path is reviewed.

The `expected_config_sha256` field is derived from a Manager-held `config_versions.config_hash`, **not** the independently observed sing-box runtime. It may not equal a runtime normalized digest. A correct marker hash is deterministic and non-secret; an attacker who knows the task ID/type can reproduce it. Host/Agent compromise, a modified kernel, missing journal bytes, live session persistence and network-path changes remain outside this checkpoint.

PostgreSQL constraints/row triggers are not tamper-proof against privileged database administrators. A Manager-owned record is independent of a remote Agent's **claim**, but it is not a hardware-backed host attestation.

The Agent credential generation is an immutable snapshot. A later credential rotation MUST cause any future comparison/dispatch to revalidate current Manager generation inside its own transaction. This migration does not prohibit legitimate token rotation and must never be used to re-enable a stale node.

## Required before enabling in the task path

1. **Safe enrollment:** design per-Agent explicit and durable enrollment states, plus a legacy migration path that does not lock out already running US/FI/NL. A journal missing on an *enrolled* host must block recovery, including when both old marker and receipt disappeared.
2. **Atomic dispatch coupling:** an opt-in dispatcher must demand a committed checkpoint matching its immutable job, server, Agent, generation, config version and epoch, and couple that check to the same locked pending→in-progress transition. Never dispatch if commit is ambiguous or audit absent. A checkpoint must not be minted from a task already in progress.
3. **Agent journal coupling:** without releasing the shared runtime mutation flock, persist a matching local intent and verify the Manager checkpoint *before* executing a mutation. The offline directory flock from ADR-0028 is not interchangeable with the existing runtime lock.
4. **Independent terminal review:** reconcile Manager origin with a fresh lock-held local observation, actual sing-box generation/listeners, scoped credential removal and existing-session closure. Human authorization is required, but is not proof of on-host truth.
5. **Crash/fault testing:** kill Manager/Agent after each commit, response, local write, `fsync`, ACK and replay boundary; include lost requests and reauthentication, corrupt/symlink/removed local journal, version edits and token rotation; never auto-unlock on ambiguity.
6. **Explicit canary and rollback review:** no default-on migration or production activation until full test matrix and installer/onboarding acceptance, including optional Grafana during installation, is completed.

## Test and rollout status

PostgreSQL tests cover Manager-derived content, config/service-specific checkpoints, unavailable or already-dispatched jobs, caller-supplied evidence rejection, immutable source identity, immutable audit, audit rollback, unique concurrent insert, destructive downgrade refusal, and empty-schema downgrade/reapply. CI also runs existing Backend/Agent/Reality/installer regressions.

All of this remains a stacked Draft PR. **No `main` merge, US/FI/NL/RU VPS action, current database write, Agent restart, or mutation-unlock capability** is performed.
