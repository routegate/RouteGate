# RG-140 — Held dispatch must have an independent database owner

Status: **security prerequisite / NO-GO for executable dispatch**, November 2026.

## Current integration checkpoint — 23 November 2026

Fresh installs now provision separate migration-owner and Manager runtime
logins, and installer/update/restore database round trips pass CI 3108 at
`815918cb`. Older deployment descriptions below are historical rationale;
legacy installations retain their original startup mode until explicit cutover.

The #695 issuance transaction (`0161fb3b`) is adapted in
`revocations/removal_dispatch_issue.go`, reusing canonical live-human
`lockRemovalDispatchActor` checks, signature verification and server-first
serialization. It creates one immutable receipt, exact held version/job and
counter increment atomically; retries read the same receipt. No production
route calls it, and canonical migration history remains 170.

Historical issuance SQL is held only in
`backend/internal/db/testdata/held_dispatch/issuance.{up,down}.sql`. The existing
opt-in disposable PostgreSQL role rehearsal exercises the writer with this
fixture, a real restricted runtime login and the canonical grant provisioner.
It covers forged signature, missing stage, runtime SQL/issuance denial,
rollback after the final write, concurrent issue, exact receipt readback,
revoked-human replay refusal, generic Agent poll denial and retained history.
The poll assertion uses the actual Agent token hash, correcting the old test's
server-ID argument. This is a source integration milestone, not deployment
support or a completed revocation operation.

Do not install the fixture SQL on an existing system. Activating a private
schema while serving with a legacy owner login correctly fails the startup
isolation guard. Production activation still requires supported credential
cutover, independently authenticated operator signing/coordinator isolation,
and the existing scoped claim/result and terminal transitions. Do not provide
owner credentials or a signing key to ordinary Manager to bypass these gates.

## Concrete current boundary

The standard `install.sh` PostgreSQL setup creates a login `routegate`,
makes it **database owner**, and supplies this same login in Manager's
`ROUTEGATE_DATABASE_URL`. `backend/internal/app/app.go::Start` calls
`db.Connect` and then `db.Migrate` on **the same pool**, before serving
requests; `backend/internal/db/migrator.go` executes migrations on that pool.
Thus, under this deployment profile, schema/table objects created by migration
are normally **owned by the Manager runtime's login**.

The reviewed historical #713 migration 176 (not yet ported) creates a private
`routegate_dispatch` schema and `held_removals` table and then executes
`REVOKE ALL ... FROM PUBLIC`. PostgreSQL object ownership is not removed by
`REVOKE FROM PUBLIC`. With the existing deployment role, this would leave
the normal Manager able to inspect/mutate its own supposedly privileged
dispatch schema. A read-only signed preflight using a supplied Ed25519 public
key does **not** fix this: production still needs an independent trusted
decision signer and an issuance role inaccessible to ordinary Manager code.

**Do not cherry-pick legacy 176 or rename it to 171 without actual role
separation and new CI tests.** A false isolation claim here could let a
compromised Manager bypass the separation between original human consent and
a privileged Agent task.

## Required design before enabling held jobs

1. Provision a dedicated **migration/object-owner role** and a distinct
   least-privileged **Manager runtime login**; a third, independently
   authenticated coordinator may hold narrowly scoped dispatch/receipt grants.
   The Manager runtime must not be an object owner, database superuser,
   `CREATEROLE` or member of the privileged roles.
2. Move startup migrations to a privileged, explicit **one-shot migration
   phase** (not the ordinary Manager pool). Specify atomic upgrades for
   already-installed systems, never silently rotate or break their live DB
   credentials. Role setup, backup, rollback and read-only inspection must be
   separately reviewed and tested in disposable CI/VPS fixtures.
3. Test PostgreSQL ownership/ACLs with a **real runtime connection**, not
   only the migration owner's connection. The runtime must be unable to
   `USAGE` or access `routegate_dispatch` tables, INSERT receipts, replay a
   signed permission, change held jobs into generic `pending` jobs, or
   discover the coordinator's private key. The signing key must not be
   derived from existing Manager secrets, user input, or the Manager database.
4. Protect even under crashes: a committed held receipt must be immutable,
   retried without re-issuing on ambiguous COMMIT outcomes, and invisible to
   `ClaimNextConfigTask`. An Agent can claim one narrowly scoped task only
   through a separately authenticated and bounded channel. The result must
   carry independently checked runtime evidence before any terminal
   account/subscription/UX update.
5. Preserve the established **161–170** reservation and non-executable
   confirmation semantics, maintain strict `super_admin` permissions, and
   reject downgrades that would destroy held history.

## CI guard

`backend/internal/db/credential_revocation_held_owner_boundary_integration_test.go`
checks whether `routegate_dispatch` exists. When absent (as expected for
current 170) it passes without creating issuance authority. If a future
migration creates it, the test **fails** when the ordinary Manager runtime
login owns the namespace or has schema `USAGE`. This guard is only a negative
release tripwire, **not** a substitute for runtime role separation or
end-to-end grant/denial tests.

## Deployment safety

This note changes **no live database, installer, role, credential or VPS**.
Until the independently provisioned privilege boundary is built and audited,
keep the signed decision checker as **dormant read-only preflight** and RG-140
as **NO-GO**. The earlier proven two-account Manager→Agent→Reality fixture
continues to demonstrate scoped local execution only, not authorized delivery.


## Manager startup fail-closed check

`backend/internal/db/held_dispatch_runtime_isolation.go` provides the
read-only `CheckHeldDispatchRuntimeIsolation` check, invoked from
`backend/internal/app/app.go::Start` immediately after migration and **before**
any account backfill, worker startup or HTTP serving. A current-schema 170
database has no `routegate_dispatch` namespace, so normal startup continues.
If that namespace exists but Manager's login owns it or has schema `USAGE`,
startup refuses to proceed with `ErrUnsafeHeldDispatchRuntimeRole`.
Catalog inspection errors are also fail-closed.

`TestCredentialRemovalStartupRefusesOwnerOnlyHeldDispatch` proves on
ephemeral PostgreSQL that `REVOKE ALL FROM PUBLIC` does not protect against
object-owner login, and that the guard rejects exactly this unsafe state.
It then drops the disposable schema and verifies that the guard allows
startup again. This **does not** provision the separately owned schema,
grant coordinator authority, execute an Agent job or change production roles.


## Disposable independent-login PostgreSQL ACL rehearsal

`TestCredentialRemovalHeldDispatchSeparateRolesRehearsal` runs only when
`ROUTEGATE_TEST_HELD_ROLE_REHEARSAL=1` **and** the supplied PostgreSQL database
is the disposable `routegate_ci` with a superuser CI connection. It creates
an ephemeral NOLOGIN schema owner and a distinct NOINHERIT/NOCREATEROLE
Manager runtime LOGIN with a random test-only password. The separate
`pgxpool` connects with that real runtime login, can read a granted public
`schema_migrations` table, but cannot read, insert or create inside
`routegate_dispatch`, or `SET ROLE` to the owner. A temporary owner-role
membership must immediately make the guard reject the runtime even if
NOINHERIT hides current schema privileges; revocation must restore isolation.
All disposable namespace and global test roles are explicitly dropped.

The startup boundary also rejects privileged roles (`SUPERUSER`,
`CREATEROLE`, `CREATEDB`, `BYPASSRLS`), protected-schema owner membership,
database ownership or CREATE privilege and effective SET ROLE escalation
(`session_user != current_user`) once the protected namespace exists.

This proves one **necessary ACL prerequisite** using real separate sessions,
not yet application-wide runtime grants, migration ownership transfer,
credential cutover or production coordinator isolation. **It changes
nothing on current installed VPS.**


## First opt-in one-shot migration plumbing (not deployed)

The new source-only `backend/cmd/routegate-migrate` tool requires both
`ROUTEGATE_MIGRATOR_DATABASE_URL` and `ROUTEGATE_DATABASE_URL`. They
must refer to the same host/port/database but different PostgreSQL login
identities. The tool connects to both accounts, rejects runtime membership
in the migration-owner role, rechecks the dispatch boundary, runs the existing
forward-only `db.Migrate` **as migrator**, and then uses the normal runtime
login to verify the complete migration catalog **read-only**. It does not
print database URLs or credentials, and is not packaged by the installer.

`ROUTEGATE_DATABASE_MIGRATION_MODE=external` is an explicit Manager
opt-in: startup **never calls db.Migrate** in this mode. It reads the exact
`schema_migrations` catalog, refusing absent, pending or unrecognized
migration filenames before HTTP serving. Default mode `startup` is unchanged
for all existing production installations and tests; the installer does not
set `external`.

This is *preparatory plumbing*, not a supported deployment or a credential
cutover. The role provisioner, ownership/DEFAULT PRIVILEGES, per-table
SELECT/INSERT/UPDATE/DELETE grants, secrets distribution, schema upgrades,
rollback/backup and coordinator service are still unimplemented. Never put
the privileged migrator URL into the Manager systemd environment. Prove the
new role model on an isolated database before changing a live deployment.


## Review hardening: pre-migration guard and privilege escalation tests

Manager now checks `CheckHeldDispatchRuntimeIsolation` **before and after**
its configured migration phase, so an already-created unsafe private schema
cannot receive additional DDL before refusing HTTP startup. The disposable
two-login rehearsal also grants and removes `CREATEDB` and database
`CREATE` on the normal runtime login, proving that both are rejected.
Partial CI schema setup is cleaned even if a multi-statement grant fails.
The source-only migrator treats context cancellation as failure, never as
successful completion.

## External-mode runtime owner check reused from historical Draft #647

The current schema 170 has no private held-dispatch schema. Checking only
`routegate_dispatch` would therefore allow a separate source-only migrator
to report success even if Manager still owns its database or public tables.
Canonical #812 now adapts the previously reviewed `CheckRuntimeOwnership`
preflight from [historical Draft #647](https://github.com/routegate/RouteGate/pull/647)
into the single `CheckExternalMigrationRuntimeRole` helper. Both explicit
Manager `external` mode and the source-only migrator call that same
SELECT-only gate. It refuses owner-capable membership (including
`NOINHERIT` / `SET ROLE`), elevated PostgreSQL role attributes, database or
public-schema `CREATE` grants, database/public-object ownership and active
role switching **even when the protected schema does not yet exist**.

The existing future-schema-specific held-dispatch guard remains in place;
ordinary `startup` mode and installed credentials are intentionally
unchanged. The previously tested but unmerged runtime DML-grant policy
([#639](https://github.com/routegate/RouteGate/pull/639) and
[#652](https://github.com/routegate/RouteGate/pull/652)) remains a separate
review source, not duplicated or silently incorporated in this change.
Neither this guard nor a green migration-readiness check provisions runtime
DML grants, validates a complete Manager deployment, authorizes held
dispatch, or permits a production role switch.

## Migration ledger search-path hardening (canonical #812)

Both the read-only external readiness check and the actual migration runner
now address the existing ledger as `public.schema_migrations` explicitly.
This covers CREATE, applied-version SELECT and INSERT; it does **not** rename
or rewrite any historical migration identity. The disposable PostgreSQL
`TestExternalMigrationRejectsShadowedLedgerLookup` also executes the real
migrator through a connection with a shadowing `pg_temp.schema_migrations`.
The corresponding CI result must be checked on the exact commit before release.

## Historical SQL role PR disposition (not yet integrated)

| Historical Draft | Review result for schema 170 | Disposition |
| --- | --- | --- |
| [#647](https://github.com/routegate/RouteGate/pull/647) | Owner-capable runtime refusal is adapted in `CheckExternalMigrationRuntimeRole` | Reuse complete for this guard; retain historical PR pending final audit |
| [#639](https://github.com/routegate/RouteGate/pull/639) | `ProvisionRuntimeRole` hard-codes a schema-175 table allowlist and refuses any other schema version | **Do not cherry-pick**; requires a separately reviewed schema-170 grant inventory and installer/credential design |
| [#652](https://github.com/routegate/RouteGate/pull/652) | `CheckRuntimeRequiredPrivileges` uses #639's schema-175 allowlist | **Not implemented** in canonical #812; a successful owner/readiness preflight is not proof that Manager can run with restricted grants |

These historical branches have diverged from the canonical migration history;
their original `000160_*` and later identities conflict with current-main's
forward-only migration numbering. Compare behavior and tests, not full branch
trees. Do not close, delete, merge, rebase or rewrite those branches as a
side effect of this documentation. Current role separation is still **NO-GO**
for live deployment, and executable held dispatch remains disabled.
