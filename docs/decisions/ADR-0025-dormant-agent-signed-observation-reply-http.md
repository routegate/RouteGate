# ADR-0025 — Dormant, fully MAC-bound Agent recovery observation reply

**Status:** RG-140 Draft, explicitly UNREGISTERED and non-operational.
No deployment, runtime mutation, unlock/recovery or real VPN access
revocation is permitted by this stage.

## Context

ADR-0024 added an intentionally dormant outbound Agent→Manager HTTP
contract for discovering an Agent's own waiting recovery attempt and
issuing one 45-second, auditable, single-use observation nonce. ADR-0022
already supplies the Manager's private PostgreSQL consumer: current
Agent bearer token, credential generation, reserved job and nonce are
checked in one transaction before committing `reply_matched_unattested`
or `reply_rejected` and its mandatory append-only audit.

There was no HTTP transport for Agent to return its *fresh* read-only
observation. The existing Agent proof HMAC from ADR-0021 covers
a versioned **digest claim**, but not the entire serialized observation,
including `observedAt`. Signing only the claim would leave these
remaining fields unbound by an independent HMAC.

## Decision

This PR adds a *second*, domain-separated HMAC of the **complete**
v1 JSON body:

`HMAC-SHA256(agentBearer, UTF8("routegate/rg140/agent-observation-reply/v1") || 0x00 || json.Marshal({claim, reply}))`.

Agent's `RuntimeRecoveryFullReplyMACV1` and Manager's
`FullReplyMACV1` implement exactly the same Go JSON field order and
names. A shared, credential-free fixture at
`testdata/rg140/token_bound_evidence_v1.json` includes the synthetic
claim, fixed response timestamp, process witness and expected full
response HMAC. Both independent Go modules verify the identical
digest and reject changed `observedAt`, nonce, generation, receipt,
or runtime hash. The *original* claim HMAC is still required; the
full-reply HMAC is an additional defense, not a substitute for the
immutable PostgreSQL bindings.

### Test-only HTTP exchange

- The Agent's `SubmitRecoveryObservationReplyV1` accepts only a
  **previously obtained** `RuntimeRecoveryObservationChallenge`
  and `RuntimeRecoveryChallengedObservation`. It checks the Manager
  URL, attempt/reservation IDs, epoch, Agent/server/task IDs, current
  credential generation, nonce, time window, and exact marker,
  acknowledged receipt and process-witness digests. It recomputes
  the evidence digest claim, signs both the claim and complete reply
  with the **existing Agent bearer token**, and makes one outbound
  `POST /api/v1/agent/recovery-observations/reply` through the
  existing HTTP client. It does not perform a local observation
  on its own, and has no automatic call site in the Agent Runner.
- Manager's `ObservationTransport.Reply` validates a bounded JSON
  body with no unknown or extra documents; requires
  `Authorization: Bearer rg_agent_…` **in the header**, never the
  JSON body; and verifies the full response HMAC in constant time.
  It then delegates to the *existing* atomic authenticated
  consumer, which re-checks the **current database token hash**,
  nonce, Agent generation, held reservation/attempt, and evidence
  before transitioning the one-use nonce and its append-only audit
  in the **same transaction**.
- On matching data the handler responds only
  `202 {"state":"reply_matched_unattested"}` with non-cache headers.
  An authenticated but invalid observation becomes a terminal
  `reply_rejected` record with audit, returned as a generic
  conflict. Unauthenticated, invalid-MAC, oversized, tampered,
  expired and replayed requests never grant success. No response
  echoes bearer token, raw nonce, private hashes or process
  metadata. **A 202 does NOT authorize any runtime action.**
- The client **never automatically retries** a reply if the
  HTTP response was lost: Manager may already have consumed the
  nonce and committed its audit. A subsequent replay remains
  rejected. Ambiguous outcomes require a separately reviewed
  administrative reconciliation process.

The methods are **deliberately not registered** in Manager's
`http.NewRouter` or `NewRootHandler`; the Agent's normal heartbeat
and task runner never call them. Negative regression tests protect
both no-default-activation boundaries. There is no extra port,
new credential, new PostgreSQL migration or VPN task type.

## Security and recovery are separate

The dual HMACs prove only possession of the **currently registered
Agent bearer token** and integrity of the signed response. A stolen
bearer or malicious Agent can still fabricate consistent file hashes,
process identity and timestamps. This is **not** host/OS/sing-box
attestation, not proof of the original on-disk state, and not evidence
that already established VPN sessions were terminated.

The expected local marker, acknowledged receipt and witness hashes
stored in migration 000165 remain **bearer-holder claims**; their
independent provenance at the original mutation checkpoint is
unproven. A successful observation ends its local exclusive lock
before this HTTP exchange completes, so it cannot authorize later
removal of `mutation-inflight.json`. Any eventual recovery must
re-observe under the same Agent exclusive lock **held through** a
durable, audited, crash-safe local transition. Actual VLESS
credential revocation and live session closure are different
acceptance gates.

All upstream RG-140 PRs remain Draft; no `main` merge,
production release/tag, installer activation, production migration,
US/FI/NL/RU VPS deployment, Agent restart or VPN config edits
are part of this implementation.
