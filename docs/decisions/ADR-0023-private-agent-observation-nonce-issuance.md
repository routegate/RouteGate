# ADR-0023 — Commit a one-use Agent observation nonce before disclosure

**Status:** Experimental RG-140 Draft only. This changes a *private Manager
method*, not any live Agent behavior or public Manager API.

## Motivation

ADR-0022 permits a trusted bearer token, an exact preexisting observation
nonce and a consistent reply to be checked and consumed in one database
transaction. The nonce registry from 000165 deliberately did not include
an issuer, because the expected private Agent-file digests had no
independently established provenance.

Before any authenticated polling/delivery integration we need a safe,
well-defined **issuance primitive**, without claiming more authority
than token possession actually provides.

## Decision

`recoveryobserve.ObservationIssuer.IssueFromAuthenticatedAgentClaim`
accepts the current `rg_agent_` bearer and four **claimed** SHA-256
digests in v1 format, plus the immutable pending recovery attempt UUID.
It does not accept an Agent identity, task ID, server or credential
generation from a caller-provided authorization header.

The issuer opens a PostgreSQL transaction and:

1. Resolves and locks the exact `awaiting_fresh_observation` attempt,
   immutable reservation, and currently registered Agent. Only the
   Agent whose database `token_hash` equals the SHA-256 of the
   presented token can proceed. The Agent's credential generation and
   persisted fencing support must still match the held attempt.
2. Validates that the claimed task/Agent/server match the **database**
   rows and all digests are canonical lowercase SHA-256. A claim is
   format-checked, not asserted to be a remote filesystem fact.
3. Generates 32 cryptographically random bytes as a single-use nonce.
   Inserts the nonce's SHA-256 (not the raw nonce), the exact reserved
   identities, generation, epoch, three locally-expected hash claims,
   and a database-clock 45-second expiry under migration 000165.
4. Requires the immutable `issued` audit event from the database
   trigger to commit **in the same transaction**. Only after
   successful COMMIT does it return the raw nonce to the private Go
   caller. If any validation, insert, audit or commit fails, it
   returns *no* nonce. A lost COMMIT acknowledgment is not success.

The one-per-attempt unique constraint blocks simultaneous issuing and
any later replacement even after the nonce expires or is consumed.
No implicit retry, reissue, nonce reset, TTL bypass, or hidden Agent
unlock exists.

## Transport and provenance limits

**The new method is not wired to an HTTP endpoint, heartbeat, task
queue, scheduler or UI.** A future caller must take the Agent token
from the existing authenticated bearer middleware. It must not accept
a token or Agent ID from untrusted request JSON. The intended delivery
is through Agent's existing outbound-to-Manager connection, not a
new inbound Agent control port.

The stored marker, *acknowledged* receipt, and witness digests are
**self-reported claims by the bearer holder**. A stolen token or
compromised Agent can invent them. Manager must not treat these
claims, or a subsequent HMAC response matching them, as trustworthy
independent host/OS/process attestation. The fourth envelope digest
is only bound to the later signed claim and reply because migration
000165 intentionally persists three expected hash columns.

The response from the existing private
`AuthenticatedReplyConsumer.ConsumeTokenBoundReplyV1` is still
`reply_matched_unattested`, never `recovered`. Its correctness as
a DB transaction does not solve actual credential revocation, live
process-generation binding or time-of-check/time-of-use between
Agent's observation and any future local fence release.

Future independently reviewed stages must introduce a safe
authenticated delivery surface, actual locked Agent observations,
trusted evidence-source enrollment/reconciliation, and a separate,
crash-safe and audited local-release protocol with full VPN
credential/session revocation acceptance.

## Tests and rollout

Disposable PostgreSQL integration tests (with Go race detector)
verify: one issuer wins under concurrency, committed issued audit
before secret disclosure, no second issuance, foreign/invalid
bearer and task/Agent/server claims refused, audit failure
rolling back the entire insert, and a complete private
issuer → signed claim → atomic consumer exchange.

No deployed migrations, production servers, Manager HTTP routes,
Agent mutation tasks, installers, `main` merge, release/tag, or
changes on US/FI/NL/RU are authorized. Keep this and all upstream
RG-140 PRs Draft.
