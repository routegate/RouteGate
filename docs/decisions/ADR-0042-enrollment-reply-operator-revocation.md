# ADR-0042 — Fail-closed enrollment reply on revoked or expired operator authorization

**Status:** RG-140 Draft / PostgreSQL integration TEST ONLY. No runtime enrollment, production migration, authentication endpoint, Agent Runner, installer, dispatcher or VPN mutation.

## Threat

ADR-0040 requires a separately committed operator grant and a fresh human authorization to **issue** a one-use challenge. ADR-0041 ensures an attributed, atomic database audit even if the issuing code path is bypassed. But there is a remaining time-of-check/time-of-use gap: after Manager issues a challenge, the human operator's session, role or dedicated permission may be revoked before a valid Agent reply arrives. A correctly signed HMAC would still prove possession of the *Agent's* token, not ongoing *human permission*.

An operator grant must not remain an evergreen right to finalize an enrollment attempt just because its nonce is valid.

## Decision

The disposable `rg140_test_enrollment_attempts` schema in the stacked test branch gains a **BEFORE UPDATE** reply-resolution gate:
- It only permits the `issued_unattested` → `reply_matched_unattested` or `reply_rejected` historical transitions; immutable attempt identity, grant, preparation, Agent, server and generation cannot be reassigned.
- At the resolution UPDATE, PostgreSQL re-checks the *original committed grant* and its dedicated immutable audit, valid grant deadline, still-active human user and session, `super_admin` role, and current `runtime_recovery:enrollment_challenge_issue` permission.
- It holds `FOR SHARE` locks on the grant/session/user/role/permission rows through the enclosing transaction, so a concurrent revocation transaction cannot commit *after being observed but before* the resolution commits. Updates/revocations that commit before this check cause a refusal.
- Revocation or expiry results in an error, **without consuming the nonce, changing the attempt state or adding an attempt-resolution audit**. The immutable challenge and grant history remain for human investigation. There is no automatic retry or grant reissue.
- This gate applies to every UPDATE path in the disposable schema, including older test-only Manager consumers and direct SQL. There is no reliance on calling a new Go wrapper for revocation enforcement.

The original, independently testable HMAC and nonce checks (ADR-0039), current Agent credential generation and Manager job/hold checks still apply in the signed Go consumer. This additional gate protects human consent. Matching a nonce, HMAC or genesis digest remains `Unattested` evidence only, never host attestation, enrollment activation or dispatch authority.

## Negative and controlled tests

`TestRuntimeRecoveryEnrollmentReplyRevocationV1*`, with Go race detector and isolated PostgreSQL:
1. Issue a valid challenge, sign a valid synthetic Agent reply, then revoke the operator session, expire it, disable the user, remove the super-admin role, or remove the dedicated issue permission. Every reply is rejected without consuming the challenge.
2. Issue while a short-lived grant is valid, let **the grant alone** expire while the Agent challenge remains live, and verify reply refusal without modifying the grant's history.
3. Verify even the older, otherwise correctly authenticated test-only challenge consumer cannot bypass the database's reply-resolution gate after revocation.
4. Verify a direct SQL UPDATE cannot promote a revoked challenge to `reply_matched_unattested`.
5. Control: a still-authorized, correctly signed reply is accepted only as `reply_matched_unattested` with one atomic attempt-resolution audit. Later revocation does not rewrite historical evidence or allow replay.
6. Inject a resolution-audit failure and confirm full rollback, leaving the attempt `issued_unattested`.

## Explicit limits

- **No host identity proof**: the Agent bearer can still be stolen or used to forge both local genesis anchors. HMAC is not TPM attestation, a measured boot report or a hardware-bound key.
- **No production human confirmation UI**: fixtures use an existing simulated human auth session. An actual grant endpoint must require a deliberate operator action with reviewed CSRF/MFA/session controls.
- **Not privileged-DB tamper resistance**: a PostgreSQL superuser can disable or rewrite test triggers. Least-privilege production schema ownership, separate writer roles and external/tamper-evident auditing remain unsolved.
- **Ordering matters**: an authorization revocation that commits *after* an already authorized resolution is historical policy change, not time travel. The test row locks establish database transaction ordering, not simultaneous host shutdown. Wall-clock expiries are checked at the SQL gate; no claims are made about synchronized external clocks.
- **No automatic recovery**: an ambiguous COMMIT result, network loss, VM snapshot rollback or Agent process crash cannot safely be resolved by silently reissuing a second challenge. Require explicit human inspection and an audited, separately authorized recovery design.
- **No live runtime changes**: all SQL and helper code is compiled only in `*_test.go` against disposable test schemas, not the live US/FI/NL Manager or Agents.

## Gate before canary

After CI passes, review a production-ready migration plan and least-privilege DB write surface, exact on-host dual-anchor read + Manager-origin authentication, atomic signed-reply consume, explicit operator opt-in, crash/replay matrix and legacy-node compatibility. Existing VPN connectivity remains untouched.

**Passing the tests does not authorize deploying, merging to main or upgrading live Agent nodes.**
