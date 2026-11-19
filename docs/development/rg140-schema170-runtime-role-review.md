# RG-140 — schema-170 ordinary runtime privilege review

Status: **review required; no runtime grants or role cutover authorized**.

This document is a review input for the canonical PR #812, not an
implementation of the historical #639/#652 schema-175 grant policy. It
separates the **public table inventory** (attested by
`TestRG140Schema170PublicTableInventory` in disposable PostgreSQL) from
an explicit **least-privilege** DML decision. A table existing does **not**
mean the ordinary Manager should receive INSERT, UPDATE, or DELETE.

## Source reconciliation

- Canonical migration chain: `backend/migrations/*.up.sql`, through 170.
- Current catalog: `canonicalSchema170PublicTables` in
  `backend/internal/db/runtime_role_catalog170_integration_test.go`.
- Historical #639 `runtimeTables`: 76 schema-175 identities, all granted
  SELECT/INSERT/UPDATE/DELETE; **not suitable as-is** for schema 170.
- Intersection: 63 identities. Canonical-only: 11. Historical-only: 13.
  These are **names**, not a statement that every operation is needed.

### Canonical-only tables: privileges pending operation-level review

| Migration | Relation | Proposed initial stance | Required evidence before granting writes |
| --- | --- | --- | --- |
| 160 | `traffic_report_checkpoints` | No new grants | Identify exact traffic ingestion queries and the caller |
| 162 | `vpn_runtime_recovery_reservations` | No new grants | Reservation lifecycle and authenticated writer |
| 163 | `vpn_runtime_recovery_challenges` | No new grants | Challenge issuer/consumer transactions |
| 163 | `vpn_runtime_recovery_challenge_events` | No new grants | Append-only event writer and tamper controls |
| 164 | `vpn_runtime_recovery_attempts` | No new grants | Attempt creation, outcome transition and audit |
| 165 | `vpn_runtime_recovery_observation_nonces` | No new grants | Nonce issuer/consumer and replay protection |
| 165 | `vpn_runtime_recovery_observation_nonce_events` | No new grants | Immutable event writer |
| 166 | `vpn_runtime_pre_dispatch_checkpoints` | No new grants | Pre-dispatch checkpoint writer and locks |
| 166 | `vpn_runtime_pre_dispatch_checkpoint_events` | No new grants | Immutable checkpoint audit |
| 167 | `vpn_runtime_recovery_enrollment_preparations` | No new grants | Enrollment preparation and approval boundary |
| 167 | `vpn_runtime_recovery_enrollment_preparation_events` | No new grants | Immutable enrollment audit |

### Historical-only identities — do not carry forward

`runtime_recovery_admission_release_evidence`,
`runtime_recovery_cleaned_commit_evidence`,
`runtime_recovery_commit_evidence`,
`runtime_recovery_events`,
`runtime_recovery_finalization_evidence`,
`runtime_recovery_mutation_grants`,
`runtime_recovery_observation_approvals`,
`runtime_recovery_observation_evidence`,
`runtime_recovery_preparations`,
`runtime_recovery_prepared_release_evidence`,
`runtime_recovery_release_authorizations`,
`runtime_recovery_retention_evidence`,
`runtime_recovery_terminal_evidence`.

The 63 common identities still require an **operation-level** review;
the historical four-bit grant is not evidence of least privilege.
In particular, tables holding human approval, dispatch staging, secrets,
or immutable audit events must not automatically acquire ordinary writes.

## Enforced dangerous-privilege boundary (19 November 2026)

The existing `CheckExternalMigrationRuntimeRole` now also incorporates the
negative ACL policy from #639: reject public-table TRUNCATE/TRIGGER/REFERENCES,
migration-journal INSERT/UPDATE/DELETE, and public-sequence UPDATE (setval).
It evaluates effective privileges for every reachable role, including PUBLIC
and NOINHERIT membership paths, rather than only checking object ownership.
The existing Manager external startup, dedicated migrator and held-dispatch
checks reuse this one guard; no second validator or grant issuer was added.

The existing two-login PostgreSQL rehearsal checks direct and PUBLIC table
ACLs, journal writes, allowed sequence USAGE/SELECT versus forbidden UPDATE,
non-owner NOINHERIT escalation, and recovery after grants are revoked.
This closes an actual excess-authority gap; it does not establish the minimum
per-table DML policy or provision separate production credentials. #652's
required-grant check and schema-170 grant issuance remain to be adapted.

## Required checks before implementing a runtime role

1. Trace real SQL read/write operations and their trust boundary per relation.
   Record SELECT/INSERT/UPDATE/DELETE individually, with evidence and
   whether a narrowly privileged function or separate coordinator is needed.
2. Review sequence USAGE/SELECT separately from table DML. Never grant
   sequence UPDATE/setval, schema CREATE, table TRUNCATE/TRIGGER, or default
   privileges to the ordinary Manager without a separately approved design.
3. Reuse **one** canonical allowlist for both the privileged installer-side
   grant issuer and the runtime-side required-privilege checker. Reject
   unknown public relations rather than silently granting them.
4. Use isolated PostgreSQL owner and LOGIN sessions to prove both allowed
   operations and negative cases (revoked grant, role membership, PUBLIC ACL,
   owned objects, schema access, unexpected future tables).
5. Re-run the exact-HEAD Backend CI and independent Agent/Manager traffic
   contract before proposing a merge. Do not touch production credentials.

**Release gate:** no schema 171, protected held-dispatch executor, Agent task
delivery, merge to `main`, VPS changes, or history cleanup on the strength
of this inventory alone. RG-140 and v0.2.0 remain **NO-GO**.
