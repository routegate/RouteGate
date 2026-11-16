# ADR-0018 — Challenge-bound Agent runtime observation without unlocking

**Status:** RG-140 experimental, Draft only. This is a private read-only
Agent API in Go code, **not** a network endpoint, authorization, attestation
signature, recovery executor or proof of scoped VPN credential revocation.

## Boundary being tested

The Manager now has a durable, append-only `awaiting_fresh_observation`
recovery attempt (ADR-0017). A historical result receipt or systemd service
being active cannot justify releasing the Agent's durable mutation intent.
A trustworthy recovery design must tie a **new Manager request** to
**newly measured Agent evidence** while preserving the local mutation fence.

This stage adds `tasks.ObserveChallengedRuntimeRecovery` in the Agent,
building strictly on the existing `ObserveRuntimeRecovery` observation
and its exclusive `runtime.lock` semantics. It does not change the
existing mutation dispatcher, traffic accounting, heartbeat, installation,
Manager routes or deployed configuration defaults.

## Read-only challenge binding (contract v1)

A future, separately authenticated Manager request must carry:

- unique observation, reservation and durable attempt UUIDs;
- the Manager's monotonic attempt epoch (positive, **not yet verified by Agent**);
- an independent, 32-byte base64url nonce;
- a bounded issued/expiry window of at most 60 seconds;
- the exact Manager URL/Agent/server binding and original task ID;
- the expected current Agent credential generation (positive, but **not
  independently verified by this Agent-side method**);
- SHA-256 digests of the private mutation marker, acknowledged receipt
  and the saved runtime witness.

The local function rejects non-canonical nonce encodings, malformed
identities/hashes, an identity mismatch with local Agent configuration,
expired or unreasonably long challenge windows and a cancelled context.

It then invokes the existing read-only runtime observation under the
exclusive mutation lock. The existing implementation verifies the exact
stored receipt, task/Agent/server binding, unchanged marker and receipt,
the sing-box service generation (systemd invocation, PID and monotonic
start), active configuration hash and listener health. The new function
refuses a mismatched marker/receipt/witness hash, cancellation or expiry
that occurs *during* the observation and returns only a restricted
observation, timing and challenge identifiers.

The witness digest is precisely
`hex(SHA256(json.Marshal(RuntimeRecoveryWitness)))` using the v1
Agent JSON field names, not a digest of the full service configuration.

## Explicit non-proofs and next requirements

**A nonce echoed back by Agent is not a signature or replay prevention.**
This v1 Go primitive has **no network-facing caller**, no authenticated
Manager→Agent transport, no persistent one-time nonce issuer/consumer on
Manager, no proof that the claimed credential generation matches
PostgreSQL, and no cryptographic signing key for remote attestation.
Its output MUST NOT be accepted as a release instruction or assumed to
be unforgeable. Future Manager code must independently check the held
reservation/attempt, current Agent identity and generation, measured
witness and expiry, authenticated transport, and consume nonce exactly
once in a durable database transaction.

The read-only method locks the local runtime state **during the
observation**. It deliberately releases the lock when returning. A
later, independently reviewed release protocol must re-observe or
keep an exclusive fence through the *actual local release commit*;
otherwise a time-of-check/time-of-use race remains. This stage never
removes `mutation-inflight.json`, starts a mutation task, restarts
sing-box, changes config, or disconnects any VPN client.

The method is limited to the existing supported sing-box VLESS adapter
and saved successful, acknowledged, generation-linked receipts. It
returns a generic fail-closed error on stale or unproven state. Legacy
receipts lacking a witness remain non-recoverable.

## Verification

Isolated Agent tests with the Go race detector require a valid live
observation to retain every byte of the saved state and local fence.
Negative tests cover forged/changed identities, nonce length or encoding,
hashes, expired/future windows, cancellation, changed config or
process generation, unhealthy runtime, corrupt receipt and concurrent
local mutation lock contention.

No automatic unlock, live trials, SQL migrations, issuer/consumer HTTP
route, production VPS changes, merges to main, or release tags are
authorized by green tests.
