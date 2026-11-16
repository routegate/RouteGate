# ADR-0022 — Atomically authenticate and record one Agent observation response

**Status:** RG-140 Draft implementation primitive. No HTTP route, task
dispatcher, nonce issuer, Agent restart, mutation-fence unlock, client
credential/session revocation, or production rollout.

## Background

ADR-0019 defined a dormant one-use observation nonce and a read-only
reply matcher. ADR-0021 added a separate, read-only Agent-token/HMAC
preflight. Calling those two functions in sequence would introduce a
time-of-check/time-of-use gap. In particular, a token, Agent generation,
nonce or attempt status could change between preflight and state
transition. No externally supplied Agent/Server identity is a substitute
for authenticating the current bearer in the database.

## Decision

`recoveryobserve.AuthenticatedReplyConsumer.ConsumeTokenBoundReplyV1`
combines BOTH checks into one PostgreSQL transaction.

1. Require a canonical existing `rg_agent_` bearer token, a versioned
   domain-separated HMAC-SHA-256 over the claim and a 32-byte canonical
   observation nonce. A future transport adapter MUST obtain the token
   from trusted authenticated Agent bearer middleware, never from a
   caller-selected identity field in JSON.
2. Lock the exact 000165 nonce row `FOR UPDATE`; in the same query,
   hold the Agent row `FOR SHARE` and the immutable 000164 attempt and
   reservation via key-share locks. Match token SHA-256 against the
   Agent's *current* registered token, current credential generation,
   persisted fencing capability, immutable attempt, original job,
   Agent/server, epoch, and issued, unexpired nonce. Recheck distinct
   expected marker/acknowledged receipt/witness hashes against database
   values. The original envelope digest, not represented by migration
   000165, is bound by the HMAC and compared with the reply.
3. Verify the read-only reply against the **same claim and database
   snapshot**: schema, nonce, task/Agent/server, process generation,
   runtime witness JSON digest, clock window, four evidence SHA-256
   domains. There is no inference that the Agent's host is trusted.
4. With a correct current token, valid HMAC, exact nonce and persisted
   attempt binding, record a malformed or mismatched reply as
   `reply_rejected`. Record a matching reply only as
   `reply_matched_unattested`. Both are **terminal** one-time
   states, with SHA-256 of the reply and the mandatory append-only
   audit event written by the migration 000165 trigger in the **same
   transaction**. A failed audit or uncertain COMMIT cannot produce an
   accepted result. Wrong bearer/MAC/attempt/nonce leaves the issued
   challenge unconsumed.
5. Retain the original durable recovery reservation and Agent's local
   mutation fence in all outcomes. No second nonce is minted or replay
   authorized.

The older independent `VerifyCurrentAgentTokenEvidenceV1` remains
a read-only diagnostic primitive and is **not** a prerequisite that can
grant authority. The old unauthenticated `RecordOneReply` is not used
by this method and MUST NOT be exposed as a network handler.

## Verification

Isolated PostgreSQL tests with Go race detector require exactly one
success under two simultaneous consumers, denial of replay, fail-closed
terminal `reply_rejected` for an incorrect *authenticated* runtime
reply, nonconsumption for untrusted MAC/bearer/nonce and DB-inconsistent
claims (even with freshly recomputed valid HMAC), strict expiry,
and atomic rollback on an injected audit insertion failure.

## Explicit limitations

**Current Agent bearer possession is not secure host attestation.**
A compromised Agent or stolen bearer can fabricate a correct HMAC and
self-consistent process/config hashes. This function only verifies
that presented evidence matches the current registered token and
preexisting durable expected digests, and that one nonce was spent
atomically. The origin and trusted enrollment of those expected
on-disk digests remain separately unresolved.

There is still **no network-facing caller**, secure nonce issuer,
authenticated dispatch/delivery to Agent, actual runtime
generation-authorized recovery, or release protocol. A local Agent
lock is held only while its earlier read-only observation runs, not
through any future release. No matching HMAC/nonce record may ever
clear `mutation-inflight.json`, replay a VPN mutation, or prove that
active VPN sessions have been revoked.

Keep PR #758 and the entire RG-140 stack Draft until separately reviewed
release gates pass. No `main` merge, tag, installer activation,
production migration or actions on US/FI/NL/RU VPS.
