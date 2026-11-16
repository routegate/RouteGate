# RG-140 — Dormant Manager preparation admission

**Review only; no end-user action or live Agent execution is enabled.**

This review slice isolates the old internal Manager `revocations.Service`
preparation and cancellation operations from #741 onto #802 → #801 → #798.
There is **no HTTP endpoint, no dispatch worker and no mutation of the
installed sing-box configuration**.

A call to `Service.Prepare` verifies the selected account and existing
applied version, fresh Agent generation/capabilities, no outstanding node or
Manager apply jobs, no transfer reservation and exact current-vs-applied
configuration equivalence. It then creates one immutable scoped candidate
in the dormant schema-161 ledger. The request does not accept a browser-
supplied executable command or runtime path, and it neither changes
account status, subscriptions nor version pointers. `Cancel` only
cancels an operation that was **never dispatched**.

## Routing/advisory lock order

Older #741 took advisory lock `140159` **then** issued a routing-table
`LOCK TABLE ... IN SHARE MODE`. PostgreSQL routing DML obtains its
implicit `RowExclusiveLock` **before** the schema-161 `BEFORE ROW`
trigger asks for that same advisory lock. Two concurrent transactions
can therefore acquire opposite locks and deadlock.

This slice **does not issue `LOCK TABLE ... SHARE`**. It retains the
account row → transaction-scoped advisory lock ordering used by
`transfers.StartInTransaction`. The schema-161 routing-table triggers
serialize every supported routed INSERT/UPDATE/DELETE through the
same advisory lock, and reject changes while a prepared operation
exists. A routing statement already holding `RowExclusiveLock` is
compatible with the Manager's ordinary SELECT reads. A writer waiting
for the advisory lock after preparation sees the new committed ledger
and must fail closed; a writer that commits before preparation releases
its lock is visible to subsequent preflight reads.

The disposable PostgreSQL regression
`TestRevocationPrepareDoesNotDeadlockWithRoutingRowExclusive` holds
a routing `RowExclusiveLock` on one database connection, then calls
the real preparation service through a separate connection configured
with a **server-side `lock_timeout`**. It confirms preparation succeeds
without a `SHARE` wait, that the pre-existing routing transaction
cannot bypass the new prepared ledger, and that routing succeeds after
cancellation. Existing #741 preparation tests additionally verify
idempotent retries, unrelated account preservation, Agent freshness,
staged-transfer conflicts, job admission and immutable audit records.

This resolves the specific advisory-vs-Share lock inversion. It does
**not** prove the absence of all possible deadlocks involving other
Manager/Agent workflows; independent SQL review and stressed
multi-operation concurrency tests remain necessary before activation.

## Runtime and security acceptance not completed

Preparation does **not** mean a VPN key was revoked. Manager operator
authorization, explicit shared-process-restart acknowledgement,
Agent identity authentication, trusted task/result binding, durable
outbox and retry, session-disconnect proof, unrelated-client liveness,
and schema/upgrade/reboot recovery are pending #545. Never merge this
review-only stacked PR directly to main or run it on a production VPS.
