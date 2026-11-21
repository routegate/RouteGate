# ADR-0014 — One-way recovery challenge ledger before Agent unlock

**Status: RG-140 draft, storage-only.** This checkpoint adds an internal
PostgreSQL migration and isolated tests. No HTTP route, human-facing issue
action, token delivery, runtime recovery worker, Agent unlock or actual VPN
credential revocation is implemented.

## Threat and trust boundary

A valid SHA-256 commitment does not make a challenge one-time. It must be
persisted and consumed transactionally. Likewise, possession of a token is
insufficient when a human has logged out, lost permissions, or the node
reservation no longer matches the Agent. PostgreSQL, not an old UI profile,
must verify the human and the immutable node hold.

Migration `000163_runtime_recovery_challenge_ledger` defines a narrow,
append-only ledger. Each row has an immutable recovery-reservation foreign
key, human user and session, context binding hash, opaque challenge-token
digest, 90-second issuance/expiry window and an `issued` or `consumed`
state. The plaintext token is **never** stored. SQL requires exact lowercase
SHA-256 hex fields and an issuance time near the database clock.

An insertion checks that the operator still has a nonrevoked, unexpired
session and is an active human with both the `super_admin` role and
`runtime_recovery:authorize` permission. The authorization query locks
the relevant user, session, role, role-assignment and permission rows until
transaction commit, so concurrent privilege changes cannot race issuance.
The same checks occur before a consumption transition.

A successfully consumed challenge cannot be consumed again. The only allowed
row modification is `issued -> consumed` with a database-clock consumption
timestamp strictly before expiry. Other updates, DELETE and TRUNCATE are
rejected. Every issued/consumed transition writes a separate append-only
audit event in the same PostgreSQL transaction. No record can be dropped
by schema rollback while any challenge or audit event remains.

**Deliberate fail-closed policy:** at most one challenge can ever be written
for each reservation. An expired, unconsumed challenge is not automatically
reissued or deleted; an eventual replacement protocol must be explicitly
designed, audited and tested.

## What this migration does not prove

A direct SQL UPDATE, such as the isolated test's update, does **not**
cryptographically validate a presented secret token. A future private
application repository must first verify the exact token with
`MatchesBoundChallenge`, then condition its SQL UPDATE on the precise
bound hashes, unused state, fresh database actor/session authorization
and expiry, and require exactly one affected row. It must commit the
consumption and audit *before* acknowledging it to any caller.

The ledger does not verify fresh systemd/sing-box process generation,
config digest, active session closure, device credentials, or the Agent
mutation marker. A consumed challenge is **not** permission for an Agent
to delete `mutation-inflight.json`: recovery release, operator approval
semantics and crash-safe re-observation remain distinct, reviewed steps.

## Test and release boundaries

Isolated PostgreSQL tests exercise one-use consumption under concurrent
transactions, wrong user/session, session revocation before issue or
consumption, removal of super_admin role, late issuance, immutable
history, no TRUNCATE and no challenge reissue on the same reservation.

Neither the installer nor web UI surfaces this ledger. There is no
production writer. No production migration is executed on US/FI/NL,
no release or `main` merge is implied by green CI. A future endpoint
must not be added until the application repository rechecks the current
operator in a DB transaction, authenticates the challenge secret, and
binds fresh Agent observation to a crash-safe, audited release protocol.
