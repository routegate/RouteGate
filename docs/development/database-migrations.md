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
preparation ledger has consumed `000161_*`, so any next migration must use
an unused ordinal **162 or higher**; never edit or replace the old 160.

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
dormant preparation/global routing guards. The standard healthy-core
account modification still works when no preparation exists.

**Do not infer completeness from this schema migration.** A future
authorized operation needs durable Manager/Agent evidence, a scoped
pinned-config apply and verified session semantics, and full tests under
#545. Existing real databases must not be upgraded without release
approval, a representative backup/restore rehearsal and independent
review of the SQL triggers.

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
