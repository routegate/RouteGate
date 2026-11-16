# RG-140 — read-only dispatch claim observation (review only)

> **NO-GO:** No Manager HTTP route, Agent dispatch, result ingestion, retry,
> credential removal, background task, runtime restart, or production database
> mutation is introduced by this change.

## Why this slice exists

The schema-163 claim is written before any possible Agent send and starts in
`outcome_unknown`. If Manager crashes after an ambiguous commit, it must
**inspect durable history**, never infer that an HTTP timeout proves failure.
`DispatchClaimReadback.Observe` is an internal, authenticated read-only
view over the schema-163/164 ledger.

For the original authenticated super-admin (future routes must run
`auth.Middleware` and revalidate the active session), it returns only:

- `outcome_unknown` with the immutable claim ID and PostgreSQL timestamp,
  after validating its stored principal, server, Agent, generation, baseline
  version and configuration/runtime hashes against the parent preparation;
- `not_observed` with no claim ID **when no committed claim is visible at
  that database snapshot**. It is NOT a proof that no request was sent, no
  mutation ran, or that any new send would be safe.

An unrelated, anonymous, improperly privileged, or malformed lookup is denied.
No request may supply another actor or claim identity.

## The critical race, now covered by an actual PostgreSQL test

Connection A inserts a valid claim but does not commit. Connection B observes
`not_observed` because READ COMMITTED cannot see A's uncommitted row. A then
commits; B observes `outcome_unknown` and any repeated Claim is denied. This
is deliberate: **even a successfully completed negative read may not
authorize re-dispatch**.

Both readback calls leave claims, Agent tasks, account status, runtime state,
and other historical rows unchanged.

## Not compatible with old task-result readback by implication

Draft [#745](https://github.com/routegate/RouteGate/pull/745) persists
results for **existing Agent task IDs**; Draft
[#746](https://github.com/routegate/RouteGate/pull/746) reads back stored
historical success for eligible legacy jobs. Neither establishes that a
schema-163 `claim_id` maps to an authenticated Agent task and immutable
credential-removal intent. A 200 response from the old verification endpoint
MUST NOT be used to mark a new RG-140 claim resolved.

Stacked Draft [#810](https://github.com/routegate/RouteGate/pull/810)
now reserves a unique, immutable **future task ID** for an existing claim
(schema 165). The database copies Agent identity/generation and configuration
hashes from the claim, but **does not create an Agent job, send anything,
authenticate an Agent receipt or make the reserved ID dispatchable**.
Even a successful schema-165 reservation is not proof of execution.

Further review must still bind this reserved ID to an authenticated Agent
envelope and verified task receipt, prevent redelivery after lost replies,
match the exact operation/configuration digests, and prove real client/session
effects. A 200 from the legacy #746 endpoint alone cannot prove that a
schema-165 reservation succeeded remotely or that cached VLESS credentials
were revoked.

The readback service intentionally provides **no** `CanRetry`, `Send`,
`ConfirmSuccess`, `Resolve` or `Unlock` API. No operator-facing route is
mounted and no automatic reconciliation is enabled. All PRs remain Draft;
RG-140 and v0.2.0 are **NO-GO**.
