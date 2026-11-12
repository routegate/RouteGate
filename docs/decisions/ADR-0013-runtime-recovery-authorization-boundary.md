# ADR-0013 — Recovery authorization and challenge binding: foundation

**Status: Draft-only RG-140 design.** This stage does **not** implement a
recovery endpoint, issue any live challenge, consume a challenge, or unlock
an Agent. It is built on ADR-0012's dormant, immutable node reservation.

## Security decision: use a distinct privilege

A general authenticated Admin session is **not** enough to release a
crash-recovery fence. Neither `configs:rollback` nor `system:manage`
substitutes for deliberate human recovery authority. This stage introduces
`runtime_recovery:authorize` to the built-in permission catalog.

The permission is seeded to **super_admin only**. It is explicitly excluded
from the broad built-in `admin` role even though that role receives most
other permissions. A manually granted permission to an ordinary operator is
also insufficient for this initial RG-140 policy. A candidate human must
be active, have the super_admin role and dedicated permission, and have a
nonempty authenticated user and session ID.

This Go policy is a prerequisite for an eventual transactional authorization
service. It does not replace re-reading the current user, session, roles
and permissions from PostgreSQL *inside* the same transaction as issuance
and later consumption. Neither an Agent bearer token nor a cached UI role
may pass authorization on its own.

## No authority in a bare challenge

The private `internal/recoveryauth` package builds a 32-byte CSPRNG
challenge, then returns an **unissued** plaintext candidate and a separate
commitment containing no plaintext token. The commitment includes the
exact 000162 reservation identity, node, Agent, job ID/kind, Agent
credential generation, three canonical digests, human actor, active
session and a 90-second validity interval.

A versioned domain-separated SHA-256 binding commits to all those fields.
The presented challenge is compared in constant time to its commitment.
Changing any identity, evidence field, operator session, token or validity
interval causes verification to fail.

**This is not a one-time authorization:** `MatchesBoundChallenge` merely
checks the cryptographic, identity and temporal prerequisites. It is safe to
call more than once, and it intentionally cannot release a mutation fence.
`BuildChallenge` may only become part of a future service that first
transactionally persists the commitment and checks the current human
session. The plaintext must not be returned before successful commit.

A future database consumer must atomically verify an unused, unexpired
challenge, current operator authority, exact still-held reservation and
a fresh Agent observation *under the matching Agent mutation lock*.
The database must prevent a second consumption even after a Manager crash
or lost response. Historical result replay and a healthy listener do not
substitute for present-tense proof.

## Production and evidence boundaries

No route, button, daemon, migration 000163, Agent task, release, or
`mutation-inflight.json` mutation is introduced by this stage. The
existing user/session/role seeds may be observed by isolated tests but no
release is authorized. Proof of current VPN credential or session
revocation remains outside scope.

The next RG-140 slice is a **durable one-use challenge ledger** with
database-backed session revalidation, immutable history and transactional
consumption. After that, independently review real Agent-side re-observation,
node fencing generation, audited crash-safe release and account-level
VLESS/Reality removal semantics before contemplating activation.
