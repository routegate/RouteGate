# ADR-0040 — Separate human grant for an offline one-shot Agent enrollment challenge

**Status:** RG-140 Draft / test-only transaction rehearsal for v0.2.0. No runtime enrollment, Manager HTTP route, production migration, installer flag, Agent Runner, dispatcher, release of a recovery hold or VPN mutation.

## Problem

Manager's existing `prepared_unattested` record (ADR-0034) is authorized for **preparation**, not for creating a live challenge. ADR-0037's disposable one-shot challenge issuer was intentionally a lower-level test that had no fresh operator authorization. ADR-0039 subsequently made the response HMAC check one atomic one-use transaction, but it does not authorize the challenge **issuance**.

A prepared Agent must therefore not become eligible for remote enrollment merely because an administrator approved a different earlier action, because the same account had privileges yesterday, or because a token is still valid. The one-off grant also has a bounded validity period (60 seconds by default in the rehearsal; never more than 120 seconds), checked by the Manager database clock during issuance, so a long-lived administrative session cannot revive a stale approval.

## Two distinct explicit test-only operations

**First — grant.** A new disposable `rg140_test_enrollment_challenge_grants` table stores an immutable, unique grant tied to exactly one existing Manager preparation, Agent, server, credential generation, **human user ID and session ID**. The caller must specify the exact intended action `authorize_one_agent_enrollment_challenge`. The existing preparation permission does not count: a **new** `runtime_recovery:enrollment_challenge_issue` permission is granted only in the disposable test fixture; no built-in role or production privilege is changed.

Within one PostgreSQL transaction, `grantEnrollmentChallengeRehearsalV1` rechecks the *current database* session, human account, super_admin role, dedicated permission, preparation event and current Agent generation/capability, while locking the relevant authorization and Agent/server rows. It refuses pending Manager work, a held recovery reservation or a pre-dispatch checkpoint. It inserts the grant plus an append-only test event and an attributed regular `audit_events` row **before COMMIT**. Failure of either audit rolls back the grant.

**Second — issue.** A separate invocation `issueGrantedEnrollmentChallengeRehearsalV1` requires a **committed** grant ID and the same still-active human/session/dedicated permission. It re-locks and revalidates the grant, current Agent identity, preparation, generation and quiet Manager state. It atomically records one cryptographically random 32-byte challenge as a SHA-256 nonce hash with a unique `grant_id` and `preparation_id`; both an attempt event and attributed issue audit must commit before returning the plaintext nonce. Racing issuers cannot create two attempts. The grant row is never promoted to `enrolled` and is not consumed by mutating it; one challenge per grant/preparation is enforced by unique constraints.

**Third — observe.** The earlier ADR-0039 test-only signed reply consumer can read the issued challenge and return no more than `reply_matched_unattested` after HMAC validation. It does **not** create enrollment status, authorize dispatch or unlock a recovery hold.

## Safety and limitations

- The entire implementation is compiled from Go `*_test.go` files and operates on `rg140_test_*` tables created exclusively in disposable PostgreSQL schemas. No production database migration is applied. Even the new permission exists **only in this fixture**.
- The prior unauthenticated **test-only** challenge issuer remains in the historical test suite to exercise its lower-level failure cases. It must never be used by a production code path. This PR demonstrates a separately guarded path; it does not replace or activate anything.
- A proper human authorization requires a distinct, deliberate UI action, session/role freshness and tamper-evident audit. These tests do not implement the UI, WebAuthn/MFA, real-time operator confirmation or an independent hardware root.
- Row locks serialize Manager DB transactions with participating writers but do **not** lock the Agent host or prove that a real VPN mutation is absent. Privileged DB compromise, forged Agent local anchors, stolen Agent bearer and full VM/DB rollback remain unresolved.
- A database transaction's committed operator grant records historical intent; it is **not a timeless permission**. This rehearsal rechecks current session and dedicated permission again when issuing. An expired/revoked session must not issue a challenge.
- This experiment does not couple new state to live Agent job admission; existing US/FI/NL remain untouched and must not be enrolled retroactively. Neither prepared nor granted nor signed-matched means `can_dispatch` or `can_unlock`.

## Acceptance tests

`TestRuntimeRecoveryEnrollmentOperatorGrantV1*` exercises separate explicit action, committed human/session/grant provenance, one-time issue, signed reply still unattested, invalid operator/session, role or permission revocation, disabled human, an expired grant using a short PostgreSQL-clock TTL, Agent generation rotation, a newly pending Manager job, a held recovery reservation, competing grant and issue transactions, and injected attributed audit failure at both stages. All run with Go race detector against isolated PostgreSQL; the full CI and Workspace integration must also succeed.

## Gate before anything deployable

A future production implementation requires a new reviewed permission seed/migration, immutable Manager enrollment ledger with downgrade policy, independent review of authenticated human confirmation, one-use authorization and signed challenge delivery, actual Agent-side local-file lock/read integration, crash/replay/rollback matrix and a canary opt-in plan that leaves every legacy Agent in its current mode. **Passing this offline suite alone is not authorization to merge a recovery unlock or deploy it.**
