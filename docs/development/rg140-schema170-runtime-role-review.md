# RG-140 — schema-170 ordinary runtime privilege review

Status: **schema-170 grant policy implemented; production role cutover pending**.

#639's new-role issuer and #652's required-grant check are adapted in
`backend/internal/db/runtime_role.go` and `runtime_required_privileges.go`.
`runtime_role_policy.go` is the single 74-table operation-level inventory,
shared by both implementations and the existing PostgreSQL catalog test.
The old schema-175 blanket four-operation policy is not copied. No schema
migration, new dispatcher, runtime identity, installer cutover or protected
SQL executor is introduced by this prerequisite.

## Source reconciliation and operation evidence

The reviewed baseline is canonical migrations through 170 and current
non-test repositories. SELECT is explicit for each catalog relation. INSERT,
UPDATE and DELETE are separately listed; an UPSERT needs both INSERT and
UPDATE, and SECURITY INVOKER triggers use the caller's privileges.

| Current source | Policy consequence |
| --- | --- |
| `auth/repository.go`, `auth/security.go`, `setup/handler.go`, `users/handler.go` | Auth/bootstrap INSERT and UPSERT, session UPDATE; no user DELETE. Existing RBAC SHARE locks also require UPDATE on `user_roles` and `role_permissions`. |
| `agents/*repository*.go`, `configs/*repository*.go`, `transfers/service.go`, migrations 155/157 | Job INSERT/UPDATE; config history DELETE; config-apply triggers update profiles/protocols. Immutable platform update history gets no DELETE. |
| `vpnaccounts`, `routingprofiles`, `nodegroups`, `connections` repositories | Exact account/device/preference UPSERT and assignment/delete operations; notes need UPDATE for UPSERT; presence is replace-by-delete/insert. |
| `delivery` and `observability` repositories | Delivery/telemetry/health UPSERT; notification and alert-transition appends; no blanket DELETE on those histories. |
| `maintenance/database_provider.go` | DELETE for the existing bounded retention targets, including auth sessions, heartbeats, audit/events and traffic. |
| `traffic/repository.go`, migration 111 | Checkpoint INSERT/UPDATE and locking; event INSERT/DELETE; the daily rollup trigger requires INSERT/UPDATE/DELETE on `traffic_usage_daily`. |
| `revocations/service.go`, `removal_dispatch_actor.go`, migration 170 | Strong routing/RBAC table locks require table UPDATE, including the otherwise read-only `managed_routing_rule_sets`; row locks require UPDATE on servers/agents/accounts/preparations. |
| `revocations/removal_job_binding.go`, `removal_dispatch_approval.go`, `removal_dispatch_stage.go`, migrations 161/168–170 | Binding/authorization/stage INSERT only; revocation preparation INSERT/UPDATE; audit-event INSERT is required by the existing invoker trigger. No event/approval/stage UPDATE or DELETE. |
| `recoveryauth`, `recoveryobserve`, migrations 163–165 | Challenge/nonce INSERT/UPDATE and trigger-created event/attempt INSERT. Attempts and reservations only receive UPDATE(id) for FOR SHARE / FOR KEY SHARE; their immutable-row triggers remain intact. |
| `recoveryenroll/prepare.go`, migration 167 | Preparation INSERT and trigger event INSERT; no UPDATE or DELETE. This does not mount the dormant enrollment endpoint or authorize runtime mutation. |
| migrations 162/166 and current readers | No ordinary INSERT on reservations or pre-dispatch checkpoints/events: current production code does not create them. Existing state remains readable and fenced. |

These are table-operation minima for the current code, with two narrow
row-lock column grants, not a claim that every writable application column
has its own SQL authorization boundary. Existing domain guards remain in
force. Strong table locks cannot be replaced by read-only privileges; see
[PostgreSQL 16 LOCK privileges](https://www.postgresql.org/docs/16/sql-lock.html).

## Issuance and startup behavior

`ProvisionRuntimeRole` creates only a new NOLOGIN role, transactionally; it
refuses existing roles, noncanonical schema head, missing/unexpected public
relations (including view replacements), unsafe PUBLIC privileges, and PUBLIC
DML exceeding the explicit policy. It does not repair shared ACLs, rotate
passwords, enable LOGIN or assign memberships. Only owned sequences of
insertable tables receive USAGE; no sequence UPDATE or default privileges.
The trusted deployment layer still has to separate credentials and explicitly
provision authentication. The existing `routegate-migrate` command exposes the adapted #639 option
`--create-runtime-role NAME`, using only `ROUTEGATE_MIGRATOR_DATABASE_URL`.
It first verifies the full canonical migration history, then invokes the same
issuer and exits without starting Manager. It must run from the directory
containing the matching `migrations/`. No installer invokes it automatically.
A successful NOLOGIN creation is not an authentication/cutover operation.

`CheckRuntimeRequiredPrivileges` reuses that policy via SELECT only and checks
every individual grant, narrow lock-column UPDATE and required sequence USAGE.
The Manager's existing opt-in external mode runs it after the shared authority
guard and exact migration-history check, before workers/HTTP startup. The
existing dedicated migrator also runs it after migration. Restored history
without restored ACLs now stops startup; neither path grants missing access.
Legacy startup migration mode is unchanged. This is a minimum-grant check,
not a replacement for the shared excessive-authority guard below.

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

## Enforced dangerous-privilege boundary (19 November 2026)

The existing `CheckExternalMigrationRuntimeRole` now also incorporates the
negative ACL policy from #639: reject public-table TRUNCATE/TRIGGER/REFERENCES,
migration-journal INSERT/UPDATE/DELETE, and public-sequence UPDATE (setval).
Column-only journal INSERT/UPDATE and table REFERENCES are checked too;
table-level ACL inspection alone does not see those grants.
It evaluates effective privileges for every reachable role, including PUBLIC
and NOINHERIT membership paths, rather than only checking object ownership.
The existing Manager external startup, dedicated migrator and held-dispatch
checks reuse this one guard; no second validator or grant issuer was added.

The existing two-login PostgreSQL rehearsal checks direct and PUBLIC table
ACLs, journal writes, allowed sequence USAGE/SELECT versus forbidden UPDATE,
non-owner NOINHERIT escalation, and recovery after grants are revoked.
This shared guard is retained; grant provisioning and required-grant checks
do not replace or weaken it.

## Verification and remaining release boundary

The existing opt-in two-login PostgreSQL rehearsal now invokes the actual
`routegate-migrate --create-runtime-role` command instead of hand-granting its
runtime role, and verifies that it remains NOLOGIN before test authentication. It checks each policy bit and
refusal when revoked, missing sequence/lock-column grants, read-only preflight,
refusal to adopt existing roles, rollback on unsafe PUBLIC column ACLs, unknown
tables and historical schema 175. Real repositories exercise auth seed/UPSERT,
revocation preparation/cancellation with trigger audit, traffic checkpoint and
rollup, then retention. Forbidden journal/approval/held-state writes remain
SQL permission failures. This extends the existing fixture and CI step.

Local compilation and URL-separation test passed. Exact-head PostgreSQL and
full CI results are recorded in PR #812; this document alone does not attest
release readiness. The installer/update/restore credential separation and
complete production held-delivery/terminal path remain release blockers.
No schema 171, VPS changes, merge, role cutover or source-history cleanup is
authorized by these checks. RG-140 and v0.2.0 remain **NO-GO**.
