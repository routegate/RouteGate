# PostgreSQL migration identities and ordinal safety

RouteGate's Manager records **the entire migration filename** (without
`.up.sql`) in `schema_migrations`. Once deployed, a migration filename is
part of the production database history. Do not rename, overwrite, reorder,
or delete already applied migrations to reconcile two development branches.

## Adding a migration

1. Compare the target branch against current `main` and inspect every
   `backend/migrations/*.up.sql` filename, not just the last item in a PR.
2. Select a **unique** new ordinal and descriptive name. Pair the up file
   with a matching down file where appropriate; review compatibility with
   existing databases, previous migrations, dependent SQL functions, and
   restore/rollback procedures.
3. Never reuse an ordinal from current `main`, even when the older
   migration has a different description or zero padding.
4. Run `go test ./internal/db` from `backend/` and the full RouteGate CI.
   The migration catalog is checked before any PostgreSQL SQL statement,
   including creation of `schema_migrations`.
5. Test upgrades against a disposable database at the **previous released
   schema**, not only from an empty database. A green unit test does not
   establish safe production upgrade or VPN credential revocation.

## Historical exceptions

Early RouteGate history contains intentional ordinal duplicates (including
`000003`, `000101`–`000103`, `000130`–`000131`,
`000140`–`000141`, and `000150`) and letter-suffixed filenames.
The migrator preserves the **exact, already checked-in** legacy
filenames below ordinal 160, including all 83 original names (singleton
filenames and intentional aliases). It rejects **both** a new alias for a
used ordinal and a new file claiming an unused number in that historical
range, even if it does not directly collide with a checked-in file.
These pre-SQL checks protect an already-upgraded database from executing a
new `000050_*` migration long after `000159_*` was applied. New work must
use a new forward-only ordinal. In the **schema-161 review branch**, the
preparation ledger has consumed `000161_*` and the review-only immutable
confirmation ledger uses `000162_*`; this review's one-time dispatch
fence uses `000163_*`. Future migrations require a unique unused ordinal
**164 or higher**; do not rename the existing 160/161/162/163.

The existing `000160_traffic_report_checkpoints.up.sql` filename is
also immutable, as is the unusual shipped `0001430_*` outlier. The
migrator rejects conflicting numeric ordinals at or above 160. It must
not reinterpret the old outlier as a new migration number to reuse.

One earlier migration is named
`0001430_platform_update_terminal_timestamp_proof.up.sql`. Preserve this
existing filename too. Numerical magnitude alone does not reliably
describe execution order across all historical files: the migrator
sorts filenames lexicographically.

## Dormant RG-140 schema 161 review

This branch carries `000161_credential_revocation_preparations.up.sql`
(and its matching down script) from reviewed Draft #740 onto the sealed
migration history Draft #798, **without** carrying #739's obsolete ordinal
guard. The table records scoped preparation and its audit history.
It deliberately has **no public endpoint, Agent command producer, runtime
apply, credential removal or permission to declare VPN access revoked**.

The PostgreSQL tests run (a) existing schema 160 with an Agent, account
and traffic checkpoint upgraded to 161, (b) idempotent re-run and preserved
old migration identity, and (c) concurrent transaction admission to the
dormant preparation/global routing guards. Preparation also acquires the
same per-node server row lock as ordinary apply and transfer admission, then
rejects active node-transfer reservations and unresolved configuration, Agent
service, or platform-update jobs. The standard healthy-core account
modification still works when no preparation exists.

**Do not infer completeness from this schema migration.** A future
authorized operation needs durable Manager/Agent evidence, a scoped
pinned-config apply and verified session semantics, and full tests under
#545. Existing real databases must not be upgraded without release
approval, a representative backup/restore rehearsal and independent
review of the SQL triggers.

## Schema 162 — immutable confirmation before any dispatch

`000162_credential_revocation_confirmations.up.sql` introduces a
**non-dispatching, immutable** one-row-per-operation confirmation bound to
the original schema-161 preparation. Its PostgreSQL trigger locks the
preparation row and rejects missing/cancelled preparations, wrong operator
identity, changed Agent ID/generation, different baseline version and
candidate configuration/runtime SHA-256 digest, and attempts to update or
delete confirmation history.

The internal, unmounted `AuthorizedConfirmation.Confirm` also requires
a valid authenticated super-admin context, the **exact** expected
candidate hash, and a new acknowledgement of potential disruption to
other users. An identical retry returns the original durable confirmation
instead of creating another. **The request context is trusted only if a
future HTTP handler first validates its live session via `auth.Middleware`.**

A cancellation of a **never-dispatched** preparation remains possible
after confirmation, but the confirmation history is retained and its
parent no longer qualifies for execution. A later dispatcher **must**
recheck the parent state, current config, Agent identity and generation,
credentials and actual native runtime before doing anything. It must
also reauthorize the operator and obtain separate explicit consent to
execute; confirmation is never an Agent task or proof of VPN revocation.

The real PostgreSQL checks cover unconfirmed persisted schema-161 rows
surviving 161→162 upgrades, repeat migration, confirmation idempotence,
mismatched snapshots and principals, immutable proof, a cancelled parent,
and downgrade refusal when historical confirmation exists. The new
migration neither dispatches an Agent task nor alters account,
subscription, applied configuration or active client sessions.

The future **at-most-once dispatch fence, durable outcome-unknown
reconciliation and authenticated Agent report** require a separate
forward-only migration and independent review. Never add automatic
re-dispatch on HTTP timeout; a lost acknowledgement could accompany a
completed shared-process restart. A database confirmation alone is not
the final #545 release gate.

## Schema 163 — one-time **possible** dispatch claim (NO Agent execution)

`000163_credential_revocation_dispatch_claims.up.sql` records **one
irreversible potential send per confirmed operation**, with a database-
assigned claim UUID, parent operation, exact confirmed actor, server,
Agent identity and credential generation, baseline version, confirmed
timestamp, candidate config/runtime hashes and its own audit timestamp.

The row's initial and only allowed state is **`outcome_unknown`**,
*before any network transmission*. A Manager restart or a lost HTTP
acknowledgment must never interpret it as `send_again` or `not_sent`.
One claim per operation is enforced by PostgreSQL UNIQUE on
`operation_id`; UPDATE/DELETE are rejected, and downgrade refuses
to discard recorded claims. Once a possible dispatch is claimed, a new
trigger forbids cancelling the parent preparation even if the caller
still sees `prepared`. Pending accounts remain fenced until
authenticated evidence and an independently reviewed recovery policy.

The before-insert trigger locks the **preparation record only** and
revalidates its exact confirmation, actor, Agent ID/generation,
server/baseline and hash identities, authenticated Agent heartbeat
generation and recent liveness, and the applied baseline. It deliberately
does **not** lock the `servers` row: the existing cancellation path
takes server-row → operation-row locks, so reversing that order could
create a new deadlock. The parent preparation already fences competing
Apply/transfer changes through the earlier SQL guards.

`AuthorizedDispatchClaim.Claim` is **internal and unmounted**. It
requires the trusted Manager super-admin context, exact expected
candidate SHA-256 and a **separate, explicit execution approval**. This
context is trusted only if a future HTTP handler has revalidated the
session through `auth.Middleware`; no such handler exists yet.
Concurrent calls serialize through PostgreSQL's operation row lock.
The first persists a conservative `outcome_unknown` record; every
repeated request, even the same administrator after reboot, returns
`ErrDispatchClaimAlreadyExists`, never a second send opportunity.
An ambiguous database COMMIT is treated as `outcome_unknown`, not as
permission to try again.

**This slice does not issue a job, send an Agent request, change the
VPN credential, restart sing-box or accept a result.** A persisted
claim is *not* proof that a task was sent, that a remote user lost
access, or that other client sessions survived a restart. Genuine
at-most-once network execution still requires a separate, reviewed
authenticated transport/Agent journal contract and durable result
reconciliation. The historic Agent outbox and Manager verification
ideas in #745/#746 are research inputs, not automatic merge targets.

Disposable PostgreSQL tests cover duplicate/concurrent claims,
unknown-outcome recovery refusal, stale Agent, mismatched snapshot,
unconfirmed/cancelled parent, immutable claim/cancellation guards,
refused downgrade and the real 162→163 upgrade of an already confirmed
operation **without generating a claim or Agent job**. Manual backup
restore, real client/session termination, public TLS and independent
release security acceptance remain required before #545 or v0.2.0
may be considered complete.

## RG-140 integration warning

Current `main` contains
`000160_traffic_report_checkpoints.up.sql`. The unmerged early RG-140
credential-revocation work also used ordinal `000160`; applying both
under their original names would be unsafe. Do **not** rename an applied
migration, blindly renumber the full RG-140 series, or interpret a clean
catalog preflight as proof of safe database upgrades.

Review each forward-only replacement with its SQL dependencies,
downgrade/restore semantics, integration tests, and verified data
migration path before promotion. This guard does not itself publish a
release or authorize deployment.
