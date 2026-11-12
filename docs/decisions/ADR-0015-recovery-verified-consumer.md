# ADR-0015 — Privately verify and consume one recovery challenge

**Status: Draft-only, no operational recovery.** This stage is a private
backend method that consumes a commitment created by migration 000163. It
has no HTTP route, user-facing challenge issuer, CLI command, Agent handler,
background worker, database schema change or local runtime unlock.

## Authorization requirements

Only the existing authenticated human session middleware may supply the
operator identity to `recoveryauth.Consumer.Consume`. A caller-provided ID
by itself is not authentication. The method checks that the principal
has the dedicated `runtime_recovery:authorize` grant and super_admin
role, then **re-reads** the same user's active, unrevoked, unexpired session
and live authorization from PostgreSQL within the consumption transaction.
Migration 000163 independently locks and revalidates the same actor and
role/permission rows in the database trigger at the UPDATE boundary.
Loss of authorization after a prior UI render cannot permit consumption.

A failed authorization or bad secret returns a single generic
`ErrChallengeRejected`; neither the secret nor historical task output
is included in errors or logs.

## One-use proof and commit ordering

The consumer locks the exact persisted challenge row, reconstructs the
complete reservation identity and Agent credential generation from the
database, and checks that the current Agent still advertises persisted
fencing support.

It uses the versioned, domain-separated SHA-256 challenge commitment
from ADR-0013. The stored token hash, context hash, task ID/kind, Agent,
server, reservation ID, evidence digests, operator, session and precise
90-second timestamp window must all match the presented token and
current DB state. The database clock is authoritative for expiry.

Only after this proof does the method conditionally perform
`issued -> consumed`. The DB trigger checks current authorization
again, and appends a `consumed` audit event in the same transaction.
Success is returned **only after transaction commit**. A second caller
racing the first observes an already-consumed row and cannot return
success; an audit insertion failure rolls back the transition.

A lost commit acknowledgement remains ambiguous, never positive
authorization for a second runtime mutation. Importantly, even a
successful committed consumption only proves that a **human challenge
was consumed once**: it does not authorize local Agent fence release,
prove the current process/configuration, or revoke a VPN session.

## Limits that remain before any live release

- Challenge **issuance** is still absent; tests use isolated SQL to
  insert correctly bound commitments. No secret is returned over HTTP.
- Session authentication itself belongs to the existing trusted
  middleware; do not expose this private method directly to a public
  client-supplied user/session pair.
- There is no fresh Agent challenge-response, signed generation,
  commit-time observation, local fence-release transaction, durable
  release attempt ledger, or one-way recovery state machine.
- The policy still permits one challenge ever per reserved node.
  Expired or invalidated challenges stay blocked until a future
  independently reviewed cancellation/reissue flow exists.
- A consumed challenge must never be treated as proof of scoped
  VLESS credential removal or terminated existing connections.

Use disposable PostgreSQL and HTTP fixtures only. Do not deploy
migration 000163, merge `main`, publish a release, mutate
`mutation-inflight.json`, or touch US/FI/NL/RU.
