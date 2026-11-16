# RG-140 — read-only review of a claimed Agent result

> **Review-only.** This code is not mounted to HTTP, the Agent task
> runner, the Manager's completion pipeline or a production VPS.

This slice follows [Draft #806](https://github.com/routegate/RouteGate/pull/806)
and audits what an **authenticated Agent result would need to prove**,
without treating a claimed dispatch or self-reported success as a
completed VPN revocation.

The old experimental [#745](https://github.com/routegate/RouteGate/pull/745)
saves an Agent result locally and [#746](https://github.com/routegate/RouteGate/pull/746)
checks a stored general-task completion. Neither is directly usable as
credential-removal completion while there is intentionally **no such
Manager task** in the current #806 chain. Their HTTP/Agent dispatch
paths were not imported.

The internal `AgentEvidenceReadback.Check` accepts a bounded candidate
report and an Agent bearer token **only in a hypothetical trusted
Manager→Agent transport context**. The caller must source the token
from a validated Agent authentication header. It compares that token's
SHA-256 against the **current registered Agent** in PostgreSQL, checks
its server and pinned credential generation, and locates the permanent
one-shot claim and matching immutable confirmation. It then looks up a
server-owned, applied candidate `config_versions` row with the exact
stored candidate hash and passes the durable identifiers and runtime
hashes through `VerifySuccess`. The comparison requires a changed
systemd InvocationID and strictly later monotonic start timestamp,
plus the expected shared-process restart outcome.

This read-only proof format currently assumes **claim ID = future
credential-removal task ID**. If the later dispatcher uses a different
task identity, that must be represented by a new durable, authenticated
mapping *before* enabling delivery. The report cannot invent or rebind
that mapping.

**Even a matching report is not an attestation of physical network
state.** It can be synthetic in a fixture, and Agent possession of a
bearer token does not prove that all active TCP sessions ended. The
method therefore returns only an in-memory `Matched` comparison and
SHA-256 of the exact report bytes. It does **not** update
`outcome_unknown`, create a receipt, mark an account revoked, change
applied versions, restart sing-box, or re-enqueue a task.

The disposable PostgreSQL regression creates a clearly synthetic
candidate version and report. It verifies correct binding, rejects
wrong Agent tokens and generation/identity/hash/session witnesses,
rejects malformed or duplicated-key JSON, and asserts no persistent
claim, confirmation or Agent job changes after repeat comparisons.

## Remaining release blockers

A separate, reviewed Agent journal and authenticated evidence
persistence channel must be connected to a genuine task producer;
the task ID mapping, durable receipt, Manager restart recovery, result
reconciliation, credential-update/version transition, actual
VLESS reconnection denial, unaffected-client availability and
pre-existing session teardown need true end-to-end tests.
Until those are complete, no operator may use this helper as proof
that a user's VPN credential is revoked. Never convert
`Matched` to a success status automatically. Schema 163 stays
`outcome_unknown` and no automatic resend is allowed.

Preserve the unmerged parent PRs and do not merge this stacked Draft
directly to `main` or touch US/FI/NL/RU production VPS.
