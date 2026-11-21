# ADR-0019 — Durable Manager observation nonce registry (RG-140)

**Status: Draft, no operational runtime recovery.** Migration 000165 and a
private, read-only Manager verifier are not exposed through an HTTP API,
UI, scheduler, Agent dispatcher or recovery release path.

## Why a new registry is required

ADR-0018 introduced the Agent's challenge-bound *read-only* runtime
observation. The Agent echoes a nonce with its result; this echo is NOT
a signature, authenticated transport or proof that the reported
generation came from a trusted machine. Manager must never interpret a
free-standing echo as authorization to unlock a VPN runtime.

ADR-0017 records one immutable, pending recovery attempt when a
super_admin's human challenge was legitimately consumed. We need a
different, independent single-use nonce for a later runtime observation.
A human challenge and a runtime observation nonce serve different
purposes and must not be confused.

## Decision: one nonce for one pending attempt

Migration `000165_runtime_recovery_observation_nonce` adds an append-only
registry for *at most one observation request per durable recovery
attempt*. The entry holds the reservation, node, Agent, original task,
Manager attempt epoch, Agent credential generation, three expected
local evidence hashes, SHA-256 of a 32-byte nonce and a bounded,
60-second-or-less validity window.

**Do not mechanically substitute** fields from migration 000164 into
the three expected Agent hashes. In particular:

- `mutation_intent_sha256` does not necessarily equal SHA-256 of
  `mutation-inflight.json` bytes;
- `result_receipt_sha256` may refer to a different representation
  from the local Agent receipt;
- `runtime_witness_sha256` must be proven to mean
  SHA-256 of `json.Marshal(RuntimeRecoveryWitness)` before it can be
  used to bind the ADR-0018 request.

This PR has **no production writer** because the trusted provenance
of those expected hashes has not yet been implemented. Disposable
PostgreSQL fixtures insert explicit, distinct expected digests to
exercise the verification contract without claiming real Agent proof.

A database trigger rejects an observation record for a different
attempt, task, node, Agent, or Agent credential generation. Insertion
requires the persisted `runtimeMutationFencingV1` capability and a
still-pending recovery attempt.

## Exactly one response is recorded

`internal/recoveryobserve.Verifier.RecordOneReply` is a private
Manager primitive, without any network-facing caller.

Given an identity already authenticated by a **future** Agent-transport
middleware, it requires a canonical, 32-byte base64url nonce whose
SHA-256 matches the stored digest. It locks the registry row and
compares the complete reply against the held attempt, current Agent
generation, time window, marker and receipt hashes, and a canonical
SHA-256 digest of the read-only witness. It rejects changed process
generation, unsupported schema, changed task, credential generation,
and stale/future observation time.

For an authenticated response with the correct nonce:

- a matching, well-formed reply is stored as
  `reply_matched_unattested`;
- a mismatching reply is permanently stored as `reply_rejected`.

Both transitions occur at most once, append an audit entry in the same
PostgreSQL transaction, and preserve the original request and response
hash (not raw credentials, config or plaintext nonce). A subsequent
replay is rejected even if the first reply was invalid. Wrong nonce or
expired requests fail closed without any terminal success. UPDATE,
DELETE and TRUNCATE cannot erase a final decision or its audit. A
downgrade refuses to erase any issued nonce or audit history.

## What remains unproven and explicitly forbidden

**The status `reply_matched_unattested` is deliberately not called
`verified` or `recovered`.** The current code does NOT authenticate
a remote Agent: there is no wired Manager→Agent transport, mutual
identity verification, request issuer, trusted mapping of expected
hashes, nor one-use nonce delivery. A caller-supplied transport
identity would not be security by itself; future endpoint middleware
must establish it independently before invoking the verifier.

Even an authenticated and matched response would describe only
the runtime state **at observation time**. ADR-0018 releases the
local Agent lock before returning, leaving a time-of-check/time-of-use
gap. A future release must keep or reacquire the exclusive lock and
re-observe immediately before a crash-safe, audited local transition.

There is **no credential removal, session revocation, unlock, cleanup,
recovery retry, migration deployment or production behavior** added by
this checkpoint. Passing CI cannot authorize an operation on live
US/FI/NL/RU nodes. Keep PRs Draft; no main merge or release.
