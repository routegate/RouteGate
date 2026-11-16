# RG-140 — Authenticated preparation intent, not a credential-revocation endpoint

> **Dormant review only.** This branch registers no HTTP route, grants no
> client-facing action and cannot send a task to an Agent or restart sing-box.

The internal `revocations.AuthorizedPreparation` adapter wraps the already
reviewed PostgreSQL `Service.Prepare/Cancel` from #803. It is the intended
entry point for a **future** authenticated request handler, not a replacement
for authenticating an HTTP request. Only the existing Manager
`auth.Middleware` may load and validate an active session token and attach
the fresh `AuthenticatedUser` to the request context. An arbitrary context
value is **not itself proof of a valid session**.

The adapter fails closed unless all conditions hold:

- Human, active user with a valid user and session UUID;
- `super_admin` role plus `system:manage`, `configs:apply` and
  `vpn_users:disable` permissions;
- Explicit acknowledgement of the possible **future shared-process
  restart** and interruption of other users. This acknowledgement
  does **not** authorize the eventual task dispatch;
- No caller-supplied `Actor`; the durable ledger's `requested_by`
  is taken only from the authenticated Manager user's ID.

A second authorized super-admin can cancel an intent which has **not**
been dispatched. The ledger retains its original `requested_by`,
records the cancelling super-admin in `last_action_by`, and appends
one immutable cancellation event. An identical retry from the original
principal returns the same prepared operation; a different principal
cannot take over its preparation. The adapter has **no Agent job
creation method**.

The integration test uses a disposable PostgreSQL schema 161 and the
real reservation service to prove: denial without acknowledgement,
session-derived audit identity, idempotent same-principal retry,
second-principal refusal of prepare, permitted audited cancellation,
and **zero dispatched Agent operation jobs**.

## Critical gates not yet implemented

A production-facing route must require `auth.Middleware` and refresh
operator permissions. A separate explicit, authenticated confirmation
must be tied to the *exact persisted* candidate and Agent generation
immediately before any future dispatch. A user's initial acknowledgement
of possible disruption can never be silently promoted into execution
consent. A revoked or expired session must not authorize further
actions. Cross-origin/CSRF protections, rate limits, the operator's
understanding of affected clients, and an audit of failed requests
require their own route review.

The current schema-161 ledger has only `prepared`/`cancelled` states;
it cannot persist `confirmed`, `dispatched`, `outcome_unknown` or
`succeeded`. Do not fake those transitions in `agent_operation_jobs`,
or interpret a token `404` as proof of real VPN credential removal.

Future work must design **forward-only schema 162** for an immutable
confirmation snapshot, one-time dispatch claim, Agent-origin
authentication, durable job/outbox, loss-of-acknowledgement reconciliation,
retry/reboot recovery and proof of scoped sing-box runtime removal,
while preserving unrelated clients. The Agent executor (#737) remains
dormant. #545 and the v0.2.0 release remain **NO-GO**.

Never merge this stacked Draft directly into `main`, touch active
VPS/production DB, or activate public installer Copy before #715
manual acceptance.
