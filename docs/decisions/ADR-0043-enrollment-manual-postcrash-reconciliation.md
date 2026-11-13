# ADR-0043 — Manual-only enrollment reconciliation after ambiguous Manager failures

**Status:** RG-140 Draft / isolated PostgreSQL integration tests ONLY. No production migration, API, Agent Runner, installer, host operation, runtime mutation/dispatch, recovery unlock, release or `main` merge.

## Problem and failure model

An enrollment attempt is written to Manager's database before the cryptographic nonce is delivered, and a signed reply is consumed exactly once with its audit. A Manager process or network connection can fail at several boundaries:
- after an operator grant commits but before any one-use challenge commits;
- after the challenge + audit commits but before the nonce reaches the initiating caller;
- after receiving a valid Agent reply but before the resolution transaction commits;
- after resolution + audit commits but before the response receipt reaches the caller;
- during the audit trigger, causing the transaction to roll back.

A caller receiving a timeout cannot safely infer which side of COMMIT occurred. Retrying issuance or signed-response consumption could accidentally create conflicting state. "No response" is not proof of "not committed".

## Read-only reconciliation design

A private **`*_test.go`** helper `inspectEnrollmentManualReconciliationOfflineV1` uses PostgreSQL `REPEATABLE READ READ ONLY` to read a committed Manager preparation, immutable test-only operator grant and its original attribution, and at most one committed one-shot challenge with the original issue audit and attempt-event count. It checks that all Agent/server/preparation/credential-generation identifiers agree.

The only values returned are a limited **historical** view of the Manager's durable state: preparation, grant, Agent/server IDs, generation, optional attempt ID and one of:
- `grant_only_unattested`: grant committed, no challenge/issue audit committed;
- `issued_unattested`: challenge and issue audit committed; response not resolved;
- `reply_matched_unattested`: signed response committed and audited, still NOT enrolled;
- `reply_rejected`: invalid genesis claimed with otherwise authenticated response, consumed and audited.

Every outcome includes `Unattested=true` and `ManualReviewOnly=true`. The result **never** contains the original challenge nonce, Agent bearer, signature, host-local anchors, a task permission, or an actionable retry token. The helper NEVER creates a new attempt, consumes a reply, unlocks a hold, enrolls Agent, changes permission or opens an API endpoint. It also does not require current operator authorization to *read historical evidence* within this isolated test API: a revoked administrator's past operation must remain inspectable, but the revocation still blocks its resolution through ADR-0042.

Readback rejects missing grants, incompatible preparation/Agent identity, missing/duplicate grant audits, missing/extra attempt events, mismatched resolution-event count and absent response commitment. A database superuser can still alter all local evidence; this exercise does not claim tamper-evident root or host attestation.

## Disposable PostgreSQL crash-window tests

The `TestRuntimeRecoveryEnrollmentCrashReconciliationV1*` suite tests:
1. Preparation alone cannot be reconciled as an authorized challenge. A committed grant with no attempt produces only `grant_only_unattested`, without a nonce.
2. Insert a challenge + its DB-triggered audits inside a transaction; simulate a process crash **before COMMIT** by explicitly rolling back the transaction. Readback shows only the prior grant and one attributed grant audit.
3. Commit a valid challenge, **discard the application-level issuance receipt**, then read from a fresh transaction. Manager sees one historical issued attempt, but cannot recover the plaintext nonce or create another attempt automatically. Reissue is rejected.
4. Stage an otherwise authorized attempt-resolution UPDATE inside a transaction, then simulate process loss **before COMMIT** with a rollback. Neither the terminal state nor its pending audit survives. This is a storage-boundary rehearsal, not a substitute for HMAC verification.
5. Commit a valid signed Agent response, **discard its application-level receipt**, and read from a fresh transaction. Matching and rejected responses remain separately auditable and unattested; replay is refused.
6. Inject a response audit failure. The whole resolution rolls back; readback still shows the previously issued challenge.
7. Revoke the human session after challenge issuance. Historical read remains available for manual review, while a new signed consume is refused.
8. Inject an extra historical attempt event in the *disposable test schema*. Inconsistent audit counts are rejected with a zero value. Invalid or cancelled contexts are rejected without mutation.

These are controlled transaction/receipt-loss simulations; **they do not kill a production process, reboot PostgreSQL, sever a real network or simulate a full VM snapshot rollback**. Those are separate pre-canary tests.

## Operator and recovery policy

An ambiguous response must not cause automatic nonce reissuance, retargeting, grant recreation, task dispatch or recovery hold release. A human operator must use a separately authorized, auditable diagnostic/reconciliation workflow (not implemented here) and decide how to proceed. A terminal historical `reply_matched_unattested` result is evidence of a signed and acknowledged attempt, **not** current Agent host integrity, installed runtime state or an active enrollment right.

The disposable `rg140_test_*` tables, triggers and helper do not exist in production. A future migration needs signed-off privileged DB permissions, immutable audit/downgrade design, exact host-local evidence read, a real Manager-to-Agent authenticated protocol, documented crash/reinstall/VM rollback rules, and an opt-in canary that never silently enrolls legacy US/FI/NL nodes.

**No permission to merge/release/deploy is implied by this test passing.**
