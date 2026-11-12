# ADR-0034 — Human-authorized, explicitly scoped Agent enrollment PREPARATION

**Status:** RG-140 Draft / dormant Manager-internal writer; no enrolled/active state, no new migration, no HTTP route, no Agent or runtime dispatch coupling.

## Why this is necessary

ADR-0031 created a preparation-only historical table, but there was no authenticated application operation to select one node and record its preparation. Direct SQL in isolated tests has no human-authorization semantics. ADR-0032 showed that Manager/Agent historical evidence is never itself host attestation. ADR-0033 rehearsed a checkpointed claim in PostgreSQL test code only, and **must not** be enabled just because `prepared_unattested` exists.

This Draft introduces a real, yet **unwired**, Manager-internal preparation writer. It reuses RouteGate's human session, roles, permissions and two existing audit mechanisms. It does **not** enable a protected-mode toggle or attest the host.

## Distinct permission, narrower meaning

New permission `runtime_recovery:enrollment_prepare` is seeded for `super_admin` **only**. It is deliberately separate from `runtime_recovery:authorize` used in recovery challenges, and is not implied by `admin`, `operator`, `configs:apply` or `agents:register`. The permission is an *administrative preparation* grant, never authorization to clear mutation fences.

No public endpoint or installer exposes this grant as a control.

## Transactional writer contract

`recoveryenroll.NewPreparer(pool).Prepare(ctx, agentID, expectedGeneration, principal)` accepts only:
- one explicitly chosen **canonical Agent UUID**;
- the expected current Manager credential generation (not taken from an Agent's self-report);
- an `auth.AuthenticatedUser` obtained by a **future trusted** existing auth middleware. The package does not accept a bearer string or expose a new handler.

The writer checks `auth.CanPrepareRuntimeRecoveryEnrollment` as an early refusal only. It then opens a PostgreSQL transaction and **re-reads the live session, active human user, `super_admin` role and dedicated permission**, holding shared row locks through commit. The Manager Agent/server rows are locked and their current generation is checked again under the same transaction. An expired/revoked session, missing grant, modified role, disabled Agent, lost fencing capability, stale generation, busy/held node or duplicate history is refused.

The writer inserts **only** into the existing `vpn_runtime_recovery_enrollment_preparations` table (migration 000167). Its existing database trigger derives Agent/server identity, generation and timestamp from Manager-owned rows, rejects active jobs and historical holds, and writes an immutable `prepared_unattested` event.

A second **conventional attributed** `audit_events` entry stores the human actor, Agent ID, session ID, preparation ID, generation and the fixed untrusted phase. The entry is inserted in the **same PostgreSQL transaction** as the prepared record and its immutable ledger event. Failure to write either audit aborts the transaction. **Important:** `audit_events` is a normal application audit table, not a cryptographic append-only trust root against privileged DB administrators; the schema 000167 event is independently immutable to normal SQL DML.

The result is returned only after COMMIT. If COMMIT is ambiguous or fails, the method refuses and returns **no success receipt**. A successful preparation cannot be repeated: unique Agent/server constraints and retained audit intentionally require reviewed future migration/retirement rather than arbitrary history deletion.

## Explicit non-activation safety boundary

- Even an authorized and committed row always has `phase='prepared_unattested'`. This is **not enrolled**, **not ready**, **not safe to dispatch**, and **not evidence that a local journal ever existed**.
- No Agent Runner, job polling repository, HTTP endpoint, UI, bootstrap installer, local lock, sing-box process or recovery authorization path calls the new writer.
- Legacy installations and already-running VPN traffic are unchanged.
- No automatic trusted-tip inference, journal genesis, session revocation, credential removal, terminal clearance or mutation fence unlock is implemented.
- The ordinary built-in human session/role checks are not themselves multi-factor or high-assurance operator attestation. The independent host-provenance question remains unresolved.

## Acceptance and next step

Disposable PostgreSQL tests verify super-admin-only role/grant, live session revocation/expiry, role removal and grant removal after stale in-memory principal capture, explicit generation checking, wrong/busy/held Agent refusal, one winner in concurrent requests, and rollback when the attributed audit insert fails. Legacy job updates remain allowed after preparation, confirming the mode is still dormant.

Require exact-head full RouteGate CI and Workspace Integration before further stacking.

**Next step:** an explicit, separately authenticated *real enrollment* protocol with durable Agent journal genesis, independent Manager checkpoint anchoring, lock ordering and verified rollback. Only after that may protected claims be integrated into the dispatcher for a strictly opted-in node.

No schema changes, live server changes, `main` merge or production installation are included in this PR.
