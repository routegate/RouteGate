# ADR-0024 — Dormant outbound Agent observation HTTP transport (RG-140)

**Status:** Draft / intentionally UNREGISTERED. No production route,
heartbeat hook, task dispatcher, database migration, runtime mutation,
local fence release or VPN credential/session revocation is enabled.

## Existing communications topology

RouteGate Agent already initiates an outbound HTTP(S) connection to the
central Manager using its current registered `rg_agent_` bearer token.
There is no reason to open an inbound control port on an Agent, change
VPN routing, or disguise recovery as an ordinary queued runtime mutation
task. The current Agent task pipeline must remain unchanged.

Prior RG-140 checkpoints established:

- a consumed human challenge and immutable, observation-pending attempt;
- a separately audited one-use observation nonce in PostgreSQL 000165;
- distinct on-disk byte digest domains for marker, acknowledged receipt,
  original result envelope, and saved runtime witness;
- private token-bound HMAC and atomic authenticated reply consumption;
- a private issuer which returns a 32-byte nonce only after issued-audit
  COMMIT, with at most one issuance ever per attempt.

This PR adds a narrowly scoped **HTTP adapter and Agent client contract**,
without registering it in the production router or Runner.

## Intended future two-phase exchange

1. An Agent that is already authenticated with its ordinary bearer
   requests `GET /api/v1/agent/recovery-observations/pending`.
   Manager returns either `{"pending":null}` or only that token's
   currently held attempt/reservation/task/Agent/server IDs, epoch and
   credential generation. No nonce, password, private file contents,
   evidence hash or VPN command is exposed by this discovery response.
   Discovery is read-only and does not commit an attempt or change a lock.
2. Agent derives the expected digest fields **locally**, by invoking
   the already implemented read-only
   `tasks.ObserveRuntimeRecoveryEvidenceV1`, which checks the current
   sing-box process/config and durable acknowledged receipt under the
   local exclusive runtime lock. This step is not invoked automatically
   by the new client or Runner in this PR.
3. Agent submits exactly the pending attempt ID and bounded evidence
   digests to
   `POST /api/v1/agent/recovery-observations/issue` using its existing
   `Authorization: Bearer rg_agent_…` header. Never accept a bearer
   token from the JSON body. The private Manager issuer independently
   checks the current token hash and credential generation against
   PostgreSQL, registers a cryptographically random one-use nonce and
   mandatory issued audit in one transaction, and returns the nonce
   only after successful commit. HTTP response carries `no-store`
   metadata; the Manager database stores the nonce **hash only**.
4. Agent strictly checks response identities, expected hashes, expiry,
   nonce shape and Manager URL binding, and builds the private
   `RuntimeRecoveryObservationChallenge` data structure for a future
   read-only observation step. It never changes the VPN task pipeline,
   restarts sing-box or removes `mutation-inflight.json`.
5. A later, independently reviewed exchange will need to run the
   challenged observation, sign its response with the existing Agent
   token, and submit it to the already built atomic authenticated
   consumer. **This PR does not add that delivery step or endpoint.**

The HTTP adapter rejects oversized, malformed or multi-valued JSON,
unexpected fields, malformed bearer credentials, unowned attempts and
digest/identity mismatch. Error responses never include the secret,
attempt ID, token hash or evidence. A duplicate issue returns a
generic conflict; no automatic retry is allowed because the nonce may
have committed before its HTTP response was lost.

## Deliberate no-default-activation barrier

The Manager's `ObservationTransport.Pending` and `.Issue` methods
exist as private, testable `http.HandlerFunc` adapters, but neither
`http.NewRouter` nor `NewRootHandler` registers them. The Agent
`Client.PollRecoveryPendingAttempt` and
`Client.RequestRecoveryObservationNonce` functions reuse the normal
HTTP bearer client, but are not called by heartbeat or task polling.
Dedicated negative regression tests fail if either side's standard
router/Runner is modified to enable them without a separate review.

No feature flag may be turned on to bypass that review. A future
activation must be explicit and evaluated against current live
deployment security and compatibility.

## Trust limits and failure modes

Even the entire authenticated two-phase request proves **only that
someone possessing the current Agent bearer token submitted consistent
hash CLAIMS**. It is not independent remote host attestation.
A compromised Agent or stolen bearer can fabricate marker, receipt and
runtime witness values. The Manager still lacks an independently
trusted provenance registry of the on-disk evidence from the moment
the protected mutation originally occurred.

The local Agent mutation lock ends after read-only observation; it is
not held through any release. A future fence-removal operation must
reobserve under an exclusive lock through a durable, audited local
commit, handle Manager/Agent crashes and ambiguous network results,
and verify actual scoped VPN credential removal and live session
termination. A nonce, HMAC or `reply_matched_unattested` status never
authorizes an unlock or a mutation replay.

The user's working VPN and production Manager must not be used as a
test environment. All acceptance here uses isolated PostgreSQL,
mock outbound HTTP, race tests, installer/traffic regressions and
Workspace Integration. No `main` merge, tag/release or US/FI/NL/RU
production changes are part of this Draft PR.
