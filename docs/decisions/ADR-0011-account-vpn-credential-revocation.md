# ADR-0011 — Subscription status is not verified VPN credential revocation

- **Status:** Proposed / design gate; no runtime revocation engine implemented.
- **Date:** 2026-10-09
- **Tracking:** [RG-140 credential revocation #545](https://github.com/routegate/RouteGate/issues/545);
  [subscription continuity #509](https://github.com/routegate/RouteGate/issues/509).

## Current confirmed behavior

1. `POST /devices/{deviceId}/revoke` and token rotation invalidate
   **subscription bearer links** only. Already imported VLESS/Reality, Hysteria2
   and other protocol credentials are not cleared from the VPN runtime.
2. `vpnaccounts.Repository.SetAccountStatus` updates a PostgreSQL account
   row (`status`, `updated_at`, `config_updated_at`). Public
   `/sub/<token>` immediately denies non-active accounts, **without**
   applying anything on the node. Bulk status updates have the same scope.
3. `DeleteAccount` deletes a database record; it does not prove the
   previously applied listener stopped accepting this account's credentials.
4. Re-activating a suspended/revoked account can allow a still-valid token
   to retrieve a subscription again. It is **not** a trustworthy way to
   restore a previously removed VPN runtime account.

Therefore the existing status badge `Suspended` or `Revoked` means **the
account record is in that lifecycle state**, not that VPN tunnels were
terminated. `Inactive` and `Expired` are not synonyms for
`verified blocked in VPN runtime`.

## First stage: truthful UI (separate PR)

- Explicit confirmations on suspend/revoke/delete, **including bulk**.
- Display the difference between blocked subscription retrieval and
  unverified runtime credential removal while account is suspended,
  revoked or expired. Show the existing Protocols/Apply section as the
  next place to inspect, **not** an auto-apply or a completed badge.
- Account status success response says *status changed* rather than
  *VPN access disabled*. Reactivation warns existing unexpired tokens
  can begin working again.
- No automatic cleanup, extra node operations, entitlement reissue,
  revocation of another device, or old-token redirects.

## Required future runtime operation (#545)

Suggested durable states:

```text
REQUESTED → PREFLIGHT → PREPARING → APPLYING → VERIFYING → CONFIRMED
                   ↘ BLOCKED / FAILED / RECOVERY_REQUIRED
```

Use a transactionally persisted operation with explicit actor, exact
account and source-node ID, pinned applied config version/hash, apply
job ID, protocol identity and timing information.

### Safety invariants

- **Scope:** one account, one eligible VPN/Hybrid node, initially applied
  VLESS/Reality. No assumptions about geographic direction, node name,
  443 vs 8443, or local version ordering. US Hybrid must retain Manager,
  HTTPS, Agent, and unrelated protocols.
- **Preflight:** reject stale/unknown applied snapshot, absent Agent,
  conflicting transfer, apply/maintenance reservation, or unrelated
  unapplied saved changes. Never deploy all saved edits merely to revoke
  this account.
- **Renderer:** construct a controlled config from the proven current
  applied baseline, removing **only** the selected account's specific
  deployed VLESS UUID while retaining all unrelated accounts and
  listeners. Apply must be version-pinned, validated and atomic.
- **Proof:** Agent-confirmed apply for expected version+hash, applied
  snapshot **absence** of revoked identity, unchanged membership for
  all unrelated users, runtime/listener checks, and protocol-specific
  analysis of pre-existing sessions. No `confirmed` on timeout, status
  update, heartbeat or HTTP 200 alone.
- **Failure:** if apply fails or proof is missing, the UI must say runtime
  state **unknown/pending**, retain the failed operation and guide
  explicit recovery. Restore of previous config may re-enable
  credentials and must never be silently automatic outside carefully
  designed bounded rollback semantics.
- **Concurrency:** protect target account/node from reactivation,
  deletion, transfers, fresh assignments or unrelated config applies
  while an operation is active; survive Manager restarts without
  replaying jobs.
- **Security/audit:** least privilege, scoped operator confirmation,
  no bearer links/private keys/UUIDs in logs, no raw shell input,
  no account tokens rotated by default.
- **Testing:** two-account protected baseline; one removed, one still
  served. Include failed apply, stale versions, Agent disconnect,
  competing changes, Manager restart, repeated requests, and existing
  client sessions in an isolated sing-box test before any live testing.

### Per-device limitation

Devices currently have **individual subscription URLs** but share
the VPN Account's protocol credential (e.g. VLESS UUID). Consequently,
device-token revocation must **not** be shown as disconnecting only that
device from the VPN. True device-level termination needs a separate
per-device VPN identity design and cannot be emulated by revoking the
shared account credential (which would disrupt other devices).

### Unsupported / shared-protocol caution

A node-wide MTProto secret or other shared credential cannot be
revoked for one user without impacting others. The future feature must
explicitly refuse unsupported modes, rather than overclaim isolation.

## Rollout boundaries

All of this ADR is **design**, not an executable revocation endpoint.
No production migrations, Agent restarts, token rotations, FI tests or
Manager updates are approved by this document. The first PR improves
only the warnings/status UX; follow-on backend work requires review
and isolated end-to-end validation.

## Forward-port checkpoint (2026-11-12, draft only)

The v0.2.0 reconciliation carries dormant implementation slices in draft
PRs [#742](https://github.com/routegate/RouteGate/pull/742),
[#743](https://github.com/routegate/RouteGate/pull/743),
[#744](https://github.com/routegate/RouteGate/pull/744) and
[#745](https://github.com/routegate/RouteGate/pull/745).
They do **not** establish an operational credential-revocation lifecycle.
The public Agent and Manager have no enabled credential-removal route.

### Experimental fencing and durable result delivery

The Agent's `experimental_runtime_mutation_fencing` option is deliberately
disabled in normal configuration and is not part of the installer. An Agent
which explicitly opts in negotiates the Manager's persisted
`runtimeMutationFencingV1` capability before executing fenced mutations.
The Manager then preserves uncertain jobs rather than marking them failed
by elapsed time alone. An existing local runtime-mutation state keeps the
protection active across restarts and configuration changes.

Under a private, exclusive runtime state lock, the Agent saves its successful
completion envelope **before** the first HTTP delivery attempt. The bounded,
fsynced receipt is bound to the Manager URL, Agent and server identities,
job ID and exact mutation-intent digest. The receipt may include sensitive
operation output and must never be logged or exposed to the browser. HTTP
500→404 is **not** positive result acceptance. The Agent may retransmit the
exact saved report without re-executing the runtime handler, but it cannot
use that historical result to unlock the fence or certify current VPN
configuration or terminated sessions.

These drafts provide no authenticated operator reconciliation, no verified
current-runtime observation, no complete scoped credential removal dispatcher
and no detached-worker handoff. After an uncertain outcome, **do not delete
the local state, reset the database job, or force-restart a runtime to
work around the fence**. The supported recovery protocol is still to be
designed, independently reviewed and tested.

Only isolated disposable host / PostgreSQL / HTTP-fixture tests are
authorized at this stage. Successful CI is not permission to activate
this experiment on US/FI/NL/RU, merge into `main`, publish v0.2.0 or
claim that a cached VPN credential has been revoked.

### Manager historical-result verification (seventh implementation slice)

`POST /api/v1/agent/tasks/{job_id}/result/verify` is an authenticated, read-only
observation endpoint for Agents with persisted `runtimeMutationFencingV1`.
It accepts the original bounded success envelope and compares its structured
payload with the stored successful config/service/install/maintenance job.
It requires the current Agent token and matching Agent/server ownership, an
empty stored error, and exactly one eligible job. It neither rewrites the result
nor updates job timestamps or lifecycle state. Unsupported task kinds, unknown
jobs and foreign identities cannot yield positive evidence. Responses are
`Cache-Control: no-store` and contain no stored report or credentials.

The response binds schema version, verified flag, task/Agent/server IDs and a
SHA-256 digest of the exact submitted envelope bytes. Payload comparison uses
PostgreSQL JSONB equality (object order and equivalent numeric formatting do not
matter), with `json.RawMessage` preserving numeric values during verification.
Omitted/null payload follows the existing completion contract's empty object.
This does not repair old payloads rounded by legacy completion decoding: if a
stored value differs from the Agent's durable envelope, verification fails
closed rather than guessing that they were equivalent.

When durable result delivery fails, Agent can use this observation to recognize
that Manager already committed the result before its HTTP response was lost.
It verifies all response bindings before marking the local receipt acknowledged.
An older Manager, rejected request, mismatched report, non-success job or missing
evidence leaves the receipt unacknowledged. No runtime handler is retried.

This closes the lost-response delivery ambiguity only. It does **not** authorize
clearing the local mutation marker, assert current runtime health, confirm VPN
credential revocation or terminate existing sessions. The fence remains held
logically after restart until a future authenticated reconciliation operation
checks fresh runtime evidence and explicitly authorizes recovery. No operator
unlock route is introduced here; the experimental mutation mode remains off.

Tests cover the Agent's rejection of identity/digest/schema mismatches and prove
that verified replay retains the fence. PostgreSQL tests exercise actual Manager
completion and verification handlers: initial commit, duplicate completion 404,
successful historical readback, unchanged result/timestamps, payload mismatch,
large exact integers, non-success state, foreign token and legacy capability.
