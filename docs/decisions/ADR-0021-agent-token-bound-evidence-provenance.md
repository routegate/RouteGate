# ADR-0021 — Bind recovery evidence to the current Agent bearer (RG-140)

**Status:** Experimental, Draft-only. No public HTTP handler, Agent
dispatcher, nonce issuer/consumer integration, runtime unlock, migration
or deployed VPN changes.

## Existing authentication, not a parallel trust system

RouteGate already registers every Agent with a 32-byte random bearer
token prefixed `rg_agent_`. The Manager keeps only its SHA-256 and
uses it for existing Agent API authentication. A separate ad-hoc
password/identity scheme for recovery would increase attack surface.

This stage introduces a **read-only token-possession preflight**. The
Agent can compute an HMAC-SHA-256 over a stable, versioned v1 evidence
claim with its existing bearer token. The claim contains:

- the observation, reserved node and immutable attempt identifiers;
- the Manager-side attempt epoch and claimed Agent credential generation;
- the 32-byte observation nonce (base64url without padding);
- exact task, Agent and server IDs;
- the four byte-domain-specific SHA-256 values from ADR-0020:
  marker, **acknowledged** receipt, original result envelope and
  serialized saved runtime witness.

The HMAC input is the UTF-8 domain
`routegate/rg140/agent-token-bound-evidence/v1`, a zero separator
and `json.Marshal(v1Claim)` in its documented Go struct field order.
The token itself is never placed inside the evidence object, log or
database record. Manager re-computes the MAC in constant-time
comparison and checks the bearer SHA-256 against the Agent's **current**
PostgreSQL `token_hash` under a short-lived read transaction.

The Manager also checks the same Agent/server, immutable pending
attempt, attempt epoch, current Agent credential generation, persisted
fencing capability, still-issued nonce, DB-based expiry and matching
nonce/marker/receipt/witness hashes. It never interprets an arbitrary
caller-supplied Agent ID as authentication.

An independent synthetic (nonproduction) token and claim/MAC vector
at `testdata/rg140/token_bound_evidence_v1.json` is used by both
Go modules. Isolated PostgreSQL tests require that holders of a
different validly formatted bearer, or holders of the correct token
who forge claim fields and recompute a valid HMAC, **cannot bypass**
the persisted node/attempt/nonce/generation boundary.

## Why this is still NOT remote attestation

**HMAC only proves that the sender knew the current bearer token.**
It cannot prove the sending process was a particular Agent binary,
that the host OS is uncompromised, or that a reported process
generation or file digest corresponds to current on-disk bytes. A
stolen token or compromised Agent can fabricate an internally
consistent HMAC and observation. Secure HTTPS transport, careful
secret handling and token rotation policies remain prerequisites.

This Go method is **read-only**. A successful call does not consume
the nonce, change the pending attempt, authenticate a human operator,
or authorize any Agent runtime operation. Its PostgreSQL read locks
end on return, so it MUST NOT be used as a substitute for rechecking
the Agent token/generation/evidence **inside a future atomic
nonce-consumption transaction**.

The existing isolated `Verifier.RecordOneReply` from ADR-0019
still accepts a manually supplied transport identity and remains
**unwired**. It must not be exposed directly through any HTTP route.
Future integration must replace this with an authentication-first
adapter that uses the actual bearer token and verifies the bound
evidence/nonce under a single transaction. Before releasing a local
mutation marker, Agent must independently obtain fresh proof under
its exclusive local lock and preserve durable audit and crash
recovery invariants.

There are **no changes to production schemas, HTTP routes, Agent
heartbeat/traffic reporting, installer options, VPN credentials,
active client sessions, or runtime fences**. All stacked RG-140 PRs
remain Draft; no main merge, tag/release or US/FI/NL/RU deployment.
