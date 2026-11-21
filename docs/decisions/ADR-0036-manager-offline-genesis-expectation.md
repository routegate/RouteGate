# ADR-0036 — Manager-derived offline Agent genesis expectation (unattested)

**Status:** RG-140 Draft for v0.2.0. Strictly read-only Manager observation and test-only Agent contract. Not enrollment, not activation, not Manager/Agent handoff, not proof of host identity.

## Context

ADR-0034 records a human-authorized immutable Manager preparation in PostgreSQL as `prepared_unattested`. ADR-0035 records two local canonical Agent files with identical genesis JSON, but those files can be forged together or rolled back by a privileged host. Before we add any network handshake, Manager and Agent must at least agree on **exact bytes** for the seven canonical fields.

## Decision

A new helper in `backend/internal/recoveryenroll` computes:

```text
SHA256("routegate/rg140/offline-enrollment-genesis/v1" || NUL ||
       canonical JSON(schemaVersion=1, managerPreparationId, agentId,
                      serverId, agentCredentialGeneration,
                      managerPreparationPhase="prepared_unattested",
                      nextJournalSequence=1))
```

The field order and encodings match the existing Agent's `recoveryEnrollmentGenesisDigestV1`. An immutable golden vector (the same digest computed independently by Agent and Manager Go tests) catches drift in field names, order, identity, domain prefix, integer representation or terminating bytes.

A new **private read-only** Manager `inspectRecoveryGenesisManagerExpectationOfflineV1` reads only Manager PostgreSQL rows in a `REPEATABLE READ READ ONLY` transaction. It requires an existing immutable preparation, exactly one preparation audit event, matching current Agent/server identity, unchanged credential generation, enabled fencing capability and absence of currently outstanding Manager jobs, recovery holds or pre-dispatch checkpoints in that snapshot. The caller supplies only an Agent ID. It cannot supply a genesis digest, phase, preparation ID or generation.

The returned object holds only a **historical expected digest** and `Unattested=true`. It is deliberately not called by the HTTP API, dispatcher, Agent Runner, installer, runtime mutation gate or recovery system. No new database migration, write, token, nonce, network request or host operation is introduced.

## Security boundaries

- The Manager database preparation is independent of the Agent-local anchor files. **But deriving a hash from it does not prove Agent possession**, a legitimate installation, current process state or a genuine host. A compromised Agent can forge *both* local files to match the public fields.
- The digest is **not a MAC/signature** and binds no authenticated challenge. A replayed valid genesis is still only historical `Unattested` evidence.
- A database snapshot does not lock the Agent or ensure it remains quiet after inspection. It cannot justify a pending-to-running transition or deployment.
- A privileged Manager database actor can modify trust roots/credentials outside the application invariants. A privileged host or VM snapshot can replace both local anchors. This helper does not solve either threat.
- The Manager preparation/Agent dual-file contract is not a separately committed, challenge-bound Manager enrollment root. An actual opt-in protocol still needs an independently persisted Manager anchor, authenticated live nonce exchange, one-time consumption, identity/generation verification under locks and a reviewed failure/recovery policy. Host-bound attestation needs an explicit achievable trust model, not an assertion based on hash equality.
- Even a future successful handshake MUST NOT automatically release an existing RG-140 hold or retroactively enroll legacy US/FI/NL Agent nodes.

## Acceptance

1. Both Go modules match the same exact digest golden vector with `go test -race`.
2. Manager-side PostgreSQL tests refuse missing preparation, stale rotation, disabled/lost-capability Agent, newly pending Manager jobs, invalid/cancelled callers.
3. No new external endpoint, migration, Agent dispatch, live local file read/write, VPN restart, credential rotation or production deployment.
4. The full exact-head CI remains green; PR stays Draft.

## Next independent work

Design the **durable Manager-side enrollment transcript** with scoped operator selection, nonce freshness and single-use audit; decide which real host identity guarantees are possible with existing Agent credentials, and which risks must be explicitly accepted. Test a crash/rollback matrix in disposable PostgreSQL and isolated Agent directories before considering any live canary.
