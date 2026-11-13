# ADR-0041 — Mandatory attributed audit at the disposable enrollment ledger boundary

**Status:** RG-140 test-only rehearsal / Draft for v0.2.0. No production migration, runtime enrollment, new HTTP path, Agent Runner, installer, dispatcher, VPN operation, rollback unlock or production database change.

## Context

ADR-0040 models a separately granted, human-authorized one-shot Agent challenge. PostgreSQL already guards direct challenge INSERTs with a trigger that refuses missing or stale grant, but the human-attributed `audit_events` records for grant and challenge issuance were previously inserted **by the Go wrappers**. A caller with direct SQL INSERT capability could bypass the attributed audit despite obeying the grant constraint.

The test-only ledger's event trigger remained transactional, but the attributed audit was not mandatory at the database boundary.

## Decision

- Extend the `rg140_test_grant_audit` **AFTER INSERT** trigger to insert BOTH the dedicated immutable grant event and the regular attributed `audit_events` record for the stored human actor and session in the same transaction. Remove the duplicate Go wrapper INSERT.
- Add `rg140_test_enrollment_attempt_attributed_audit` **AFTER INSERT** on the one-shot attempt table. It derives operator/session from the committed grant foreign key, rejects mismatched/missing grant identity and inserts the normal attributed issue audit with exact attempt UUID. The existing ADR-0037 event trigger remains. Remove the duplicate Go wrapper INSERT.
- The original **BEFORE INSERT** grant gate in ADR-0040 still rejects null/missing/stale grant and rechecks human session/permission, Agent/server/generation and expiry. Database triggers ensure that *regardless of which Go call or direct SQL INSERT initiated the operation*, successful commits always include attributed audit.
- Auditing or metadata creation failure aborts the complete transaction, including the original attempt or grant and its dedicated event. No nonce receipt can be issued from a committed unaudited attempt. These are PostgreSQL transaction properties, not eventual consistency.

## Test cases

`TestRuntimeRecoveryEnrollmentDatabaseEnforcedAuditV1*` runs with `go test -race` against only disposable schemas:
1. An intentionally direct grant INSERT still persists exactly one grant event and one matching attributed audit.
2. An intentionally direct challenge INSERT with a correctly bound current grant still persists exactly one attempt event and one matching attributed audit.
3. An injected attributed grant-audit failure rolls back the direct grant INSERT and dedicated grant event.
4. An injected attributed challenge-audit failure rolls back the direct attempt INSERT and dedicated attempt event.
5. Existing wrapper tests and one-shot negative/race tests remain green without duplicate audit entries.

## Security caveat

**This does not authenticate a SQL superuser or an already-compromised application database credential.** A privileged actor may forge the operator identifiers in a grant, tamper with DB tables or disable triggers. The purpose here is eliminating *accidental Go call-path bypass* of audit, not cryptographically proving human intent against a database adversary.

Before production: use a reviewed migration, least-privileged DB roles and explicit transaction entry points, make issuance rights independent of broad SQL write privileges, require trustworthy fresh operator approval and bind the single-use Agent response to the granted, still-current identity. PostgreSQL write-only auditing is not tamper-evident against DB superusers.

No real Agent, Manager production database, US/FI/NL VPS or legacy job/traffic behavior is changed. Parent PRs and this PR must stay Draft pending full CI.
