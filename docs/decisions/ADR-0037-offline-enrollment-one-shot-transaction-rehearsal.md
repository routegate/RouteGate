# ADR-0037 — Offline Manager enrollment one-shot transaction rehearsal

**Status:** RG-140 Draft / PostgreSQL integration tests ONLY for RouteGate v0.2.0. No production migration or live issuer, no HTTP, Agent Runner, installer, task dispatcher, protected-mode activation or recovery release.

## Why

ADR-0034 commits an operator-authorized `prepared_unattested` Manager record. ADR-0035 writes two immutable Agent-local genesis anchors. ADR-0036 derives Manager's own expected genesis SHA-256 and checks the exact Agent/Manager byte contract. None of that establishes remote host identity or single-use consent. Reusing a past digest or a local file must never start VPN operations.

The next implementation requires a separately committed Manager-owned attempt ledger before a challenge can be delivered. The transaction and crash/replay behavior should be rehearsed without changing any real schema or device.

## Experiment

Two test-only Go files under `backend/internal/db` define a disposable PostgreSQL schema:
- `rg140_test_enrollment_attempts`: exactly one attempt per immutable Manager preparation ID, Manager-derived Agent/server UUIDs, credential generation, expected genesis SHA-256, **SHA-256 of a 32-byte cryptographically random nonce** (never the plaintext), time-limited `issued_unattested` state, and an optional terminal `reply_matched_unattested` or `reply_rejected` state.
- `rg140_test_enrollment_attempt_events`: audit inserted by an AFTER INSERT/UPDATE trigger *in the same transaction*. If audit fails, the issuance or consumption rolls back.

Issuer: within a PostgreSQL transaction, lock the Agent and server rows; obtain the committed, independently prepared Manager tuple and exactly one preparation event; verify current credential generation/fencing and absence of Manager holds, checkpoints and outstanding work; derive the SHA-256 using the ADR-0036 Manager helper; insert the Manager attempt plus audit; **only then** disclose the one-use challenge in the test result. Concurrent issuers cannot both commit because the preparation ID is unique. A failed or ambiguous COMMIT returns no challenge receipt.

Consumer rehearsal: validate the supplied exact 32-byte canonical base64url nonce and current Agent bearer token **against Manager-held token hash**, match the immutable attempt identity/generation, check expiration and outstanding work, then atomically change the one-time attempt state and append its audit. Wrong bearer or unknown nonce cannot consume the attempt; a valid challenge with the wrong genesis digest is consumed permanently as `reply_rejected`. A matching digest remains **`reply_matched_unattested`**, never `enrolled`, `ready`, `can_dispatch`, or `can_unlock`. Both racing consumers cannot succeed.

Tests verify actual expiry with a short TTL against the PostgreSQL clock, without rewriting the attempt history. Tests also inject a trigger failure on audit insertion to prove that neither issuance nor response resolution can commit without its corresponding event. Such a test fixture deliberately is not an immutable production migration.

## Unsolved security/protocol requirements

1. **Not actual enrollment.** These helpers compile only in Go test binaries and do not process live Manager/Agent traffic. The test-only ledger does not exist on a real installation.
2. **No complete authorization.** Live issuance must require a fresh, scoped human super-admin action and an auditable operator/session binding, not just a prior preparation. Existing `Prepare` authorization does not automatically authorize a future challenge.
3. **No remote authentication.** A bearer token check in a test does not attest a host, device, VM, enclave or current Agent process. The nonce is not an authenticated network handshake nor a full signed challenge/response transcript.
4. **No proven isolation against privileged rollback.** A PostgreSQL superuser can rewrite local tables; a privileged Agent host or VM snapshot can forge both local anchors. Actual host-trust and rollback semantics need an explicit threat model and independent trust root. A digest match does not solve them.
5. **No production lock or cutover.** Tests hold row locks during a transaction, but the first audit and a prior `prepared_unattested` snapshot do not fence future legacy jobs. The production enrollment state must be coupled to job creation and admission without retroactively blocking existing US/FI/NL.
6. **Ambiguous failure is manual review.** Once a challenge may have reached an Agent, neither automatic retry nor issuance of a second challenge can be assumed safe, even if no success was returned.
7. **Test schema is deliberately mutable.** Full immutable guard triggers, a migration downgrade policy, durable external provenance, revocation across Agent reinstalls, and versioned journal grammar must be reviewed before any production schema is proposed.

## Regression scope

All `TestRuntimeRecoveryEnrollmentOneShotRehearsalV1*` cases run with the Go race detector and isolated PostgreSQL:
- exact first challenge, audit, nonce hash, matching historical reply and duplicate refusal;
- incorrect bearer, malformed/mismatched nonce, matching nonce with wrong genesis digest;
- missing preparation, changed credential generation, disabled Agent, missing fencing capability, new pending job, Manager recovery hold and expired challenge;
- racing issuers and racing consumers;
- injected audit failure during issue and consume, with full rollback of state and events;
- legacy Agent job behavior unaffected by the test-only ledger.

No VPS, VPN sessions, Manager production database, real credentials, main branch or any deployment are touched.

## Next design step

Formalize a **production-ready authenticated enrollment transcript specification** with a new reviewed DB migration, live human authorization, Agent-side signed response over Manager nonce plus both local genesis anchors, expiration/replay guard, explicit opt-in transitions and crash-cut testing. Keep all operations fail-closed until that full chain is proven, and protect legacy hosts from implicit activation.
