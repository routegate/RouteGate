# RG-140 — Held dispatch must have an independent database owner

Status: **security prerequisite / NO-GO for executable dispatch**, November 2026.

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
