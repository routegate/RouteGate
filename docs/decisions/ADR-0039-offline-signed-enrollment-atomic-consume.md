# ADR-0039 — Atomic signed enrollment challenge consume (test-only)

**Status:** RG-140 Draft for v0.2.0. Disposable PostgreSQL integration rehearsal only. Not live Agent enrollment, not host attestation, not a new migration, and not an HTTP/Runner/installer/dispatcher path.

## Context

ADR-0037 models a committed, one-shot Manager challenge, expiring nonce and audit in disposable PostgreSQL. Its original test-only consumer compares the current Agent token hash and a caller-supplied genesis digest, but it does not cryptographically bind the complete response. ADR-0038 defines an independent domain-separated HMAC contract shared by Agent and Manager, covering the Manager attempt, immutable preparation, Agent/server identity, credential generation, 32-byte nonce and claimed local genesis hash.

Neither a valid MAC nor a matching local digest proves genuine Agent hardware, current sing-box process state, uncompromised runtime.lock or clean host provenance. Any holder of the existing Agent bearer could forge those assertions.

## New isolated exercise

`consumeSignedEnrollmentProbeRehearsalV1` is a Go **`_test.go`** function. It reads only the `rg140_test_enrollment_attempts` tables created in a **disposable** database fixture, never in a production migration. It:

1. Begins one PostgreSQL `READ COMMITTED` transaction.
2. Selects the still-`issued_unattested`, unexpired attempt; joins its immutable Manager preparation, Agent, and server; holds **FOR UPDATE** locks on attempt, Agent and server until COMMIT.
3. Rechecks the latest Agent credential-generation, status, fencing capability, current bearer token hash and original attempt audit.
4. Compares the signed reply's attempt UUID, Manager preparation UUID, Agent UUID, server UUID and generation with the committed Manager tuple. It does **not** allow the remote signer to select a new identity, even if they possess the Agent bearer.
5. Verifies the **complete ADR-0038 HMAC** with the current bearer *inside the same transaction*, then validates the canonical nonce against the Manager-persisted nonce hash.
6. Refuses if outstanding Manager work, recovery reservations or pre-dispatch checkpoints are found. This refusal does not touch the existing Agent jobs or either local genesis file.
7. Consumes the challenge in a single conditional UPDATE guarded by `clock_timestamp() < expires_at`. A valid MAC with incorrect genesis SHA is permanently `reply_rejected`; a matching SHA is only `reply_matched_unattested`. The existing test-only trigger inserts an audit event atomically; an audit exception rolls everything back.
8. Returns no success receipt for ambiguity, errors, expired challenges or replays.

The response commitment is a domain-separated SHA-256 of the canonical typed reply plus its MAC. The DB stores no plaintext Agent bearer or nonce.

## Verified test cases

A dedicated Go race-detector suite checks:
- one successful signed, still-unattested response; replay or reissuance refused; exactly two audit events (issue and resolution);
- changed signed fields without a matching MAC; invalid token, wrong MAC and nonce;
- a **new valid MAC from the true token holder for a different attempt, preparation, Agent, server or generation** is still rejected by the Manager-owned tuple;
- valid HMAC but wrong genesis digest consumes into a permanent rejected state;
- racing signed consumers can produce only one successful commit;
- expired nonce, rotated Agent credential generation, new pending job and held recovery reservation;
- injected failure of resolution audit insert rolls back the attempted state update.

## Security boundaries and non-goals

- The **existing** unsigned test-only consumer remains solely for its earlier rehearsal tests; the signed consumer is a separate experiment, not an operational replacement.
- A bearer-token HMAC proves possession of that token, **not physical or virtual host identity** or the correctness of Agent-local disk evidence. An attacker holding the same bearer could sign fabricated observations and has no cryptographic proof that two local files were inspected.
- The challenge issuer in ADR-0037 has **no live fresh super-admin authorization** beyond a previously prepared record. A production issuer must require a separate, explicit and auditable operator grant, and one-shot approval must not be inferred from a stale role/session.
- There is **no actual Agent-side real disk read or authenticated Manager transport**. A future Agent must authenticate the Manager, acquire both specified filesystem locks and observe canonical local anchors before signing the response.
- Transaction row locks protect the Manager's local database state *only during the consumer operation*. They do not persist as a host execution lock or stop legacy Agent operations after the transaction. This cannot justify enabling protected dispatch.
- The test ledger's audit trigger is a transactional exercise, **not** an immutable, privileged-actor-resistant production history.
- No local or PostgreSQL transaction can by itself detect rollback of an entire compromised VM, and the existing SHA-256 digest cannot establish an independent host trust anchor.
- Failed, ambiguous or expired attempts require explicit manual review, not an automatic unlock/reissue. No branch in this PR changes VPN traffic, subscription profiles, credentials, `main`, or production nodes.

## Next gate

Before any real opt-in canary, specify a durable, migration-reviewed enrollment ledger with fresh human authorization, authenticated Manager-to-Agent challenge delivery, exact current Agent credential binding, versioned on-host dual-anchor reading and a one-way, fail-closed migration protocol. Then prove recovery behavior for Manager crash, Agent crash, network loss, VM rollback, concurrent job creation, stale token and database restore. Existing US/FI/NL must remain on legacy paths unless explicitly enrolled by their operator.

**No live rollout or release acceptance is implied by passing these tests.**
