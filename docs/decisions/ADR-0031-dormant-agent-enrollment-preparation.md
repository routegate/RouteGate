# ADR-0031 — Recovery enrollment preparation must never activate an Agent

**Status:** RG-140 Draft for RouteGate v0.2.0 — Manager-side dormant preparation only.

## Background

A previous RG-140 stage (ADR-0030 / migration 000166) introduced an
immutable, Manager-origin **pre-dispatch checkpoint** for a known pending task,
without coupling it to dispatch. The offline Agent journal in ADR-0028 is
still unregistered and has no runtime reader.

A naive migration that treats a missing local journal as a clean installation
would defeat the entire crash-safety objective; conversely, enabling a
mandatory journal on already installed US/FI/NL nodes could block existing
traffic or strand an Agent before the full migration path is tested.

## Decision: a deliberately non-activatable preparation state

Migration 000167 creates **only new, initially empty tables**:

- `vpn_runtime_recovery_enrollment_preparations` — one immutable historical
  *preparation* per Agent/node, with Manager-selected `agent_id`, a Manager-
  derived `server_id`, a snapshot of current credential generation, and
  a phase that can contain **only `prepared_unattested`**.
- `vpn_runtime_recovery_enrollment_preparation_events` — mandatory append-only
  `prepared_unattested` audit, committed in the same transaction.

A future caller can supply the ID of an existing Manager Agent; no identity,
generation or supposedly verified journal hash may be supplied by an Agent.
A PostgreSQL trigger locks the corresponding Manager Agent and server rows
and requires the existing `runtimeMutationFencingV1` capability, a
non-disabled Agent and positive credential generation. It refuses a
post-event recovery reservation, an existing pre-dispatch checkpoint or
pending/running Manager jobs. These are **Manager snapshot checks only**;
they do not claim the operating system is quiescent.

The snapshot is permanent (one per Agent/node) until a new, explicitly
reviewed terminal protocol exists. Updates, deletes and truncations are
refused; a downgrade with history fails, not silently erases the record.
Audit failure rolls back preparation. There is no **enrolled**, **active**,
**trusted**, or **ready_to_unlock** state or transition in this schema.

## Existing installations are unaffected

Most importantly, neither the Agent polling/Runner nor job dispatch,
installation, login, subscription, credential rotation, or runtime mutation
admission reads this preparation table. The new migration does not backfill
rows or add any trigger to existing Agent/server/task tables.

An Agent without a preparation row retains its current behavior. Even an
Agent with a preparation row retains that behavior: **preparation is not
enrollment**. A later token rotation can make the recorded credential
generation stale; any future enrolment step must re-evaluate current
Manager generation within a new locked transaction, and must *never*
interpret old preparation as current trust.

This deliberate split permits opt-in canary preparation later without
allowing premature activation of the crash-protected admission policy.

## Gates that must precede real enrollment/activation

1. **Manager authorization**: expose a narrowly scoped operator-only action
   with an explicit, authenticated permission and audit identity. SQL
   insertion in this Draft is for isolated test fixtures; it is **not**
   operator authorization.
2. **Independent checkpoint**: require a Manager-owned and committed
   pre-dispatch checkpoint in a transaction coupled to the exact task
   `pending` → `in_progress` transition. ADR-0030's dormant schema is
   not that coupling.
3. **Agent-local durable evidence**: enroll the actual node in a way that
   binds journal genesis, Agent identity and generation, file ownership and
   exact Manager checkpoint to a durable local lock. Confirm `fsync`
   ordering and crash recovery *before* enabling mutations.
4. **Missing-journal semantics**: only a separately attested enrolled
   Agent can be expected to have a journal; for that Agent, any missing,
   corrupt or truncated journal (including loss of both old marker/receipt)
   must retain the local and Manager recovery fences.
5. **Legacy canary and rollback**: preflight existing US/FI/NL state, keep
   current VPN traffic and panel available, and design a reversibly
   staged opt-in migration for legacy nodes. Reject any update that
   automatically enrolls every `runtimeMutationFencingV1` Agent.
6. **Terminal clearance**: independent host/OS truth, fresh flock-held
   observation, operator authorization, durable Manager audit, all protocol
   credentials and established sessions; no automatic unlock on a mere
   successful hash/HMAC or matching reply.

## Threat and test coverage

The preparation ledger is a historical Manager record, **not attestation
of a real journal, OS process or network behavior**. Privileged Manager DB
operators can compromise DB evidence; an Agent bearer alone must not be
allowed to mint preparation or authorize activation.

Tests in disposable PostgreSQL verify Manager-derived identity and generation,
immutable audit, strict `prepared_unattested` phase, duplicate/concurrent
preparation refusal, legacy node operation and token rotation unaffected,
disabled/unfenced/pending/held Agent refusal, audit failure rollback and
down-migration safety.

No production schema is migrated in this PR. The entire RG-140 stack
remains Draft with no `main` merge, live Agent changes or US/FI/NL VPS
access.
