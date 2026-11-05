# RG-140 database role separation: migration entry point

Status: first implementation slice; NOT a deployed privilege boundary.
Base: #634, `15e2ccb344eebd89ebf672335a8fe004c04bdafd`. #545 stays OPEN.

The Manager previously always migrated through its serving connection. A
non-owner runtime login cannot use that startup path, even on a current schema,
because the migrator executes CREATE TABLE IF NOT EXISTS.

## Implemented

- `routegate-manager --migrate-only` applies migrations, checks the complete
  version manifest, closes its connection and exits. It does not bootstrap an
  administrator, start workers, recover jobs, backfill settings or serve HTTP.
- `ROUTEGATE_DATABASE_MIGRATION_MODE=external` makes normal startup SELECT-only
  for schema readiness, before any bootstrap/workers/HTTP. Missing history,
  missing older migrations, unexpected versions and empty manifests fail closed.
  There is no fallback to automatic migration after a failed check.
- `auto` remains the legacy default until installation and upgrade provisioning
  are ready. An unknown nonempty mode is rejected. Programmatic empty Config
  values preserve legacy behavior.
- PostgreSQL tests authenticate a separate login with only schema USAGE and
  migration-history SELECT. Readiness works, while DDL, ledger deletion,
  trigger disabling and SET ROLE to the owner are denied with SQLSTATE 42501.

Version readiness does not attest migration file contents, actual schema
integrity, runtime privileges, or successful VPN revocation. Runtime connections
must not receive migrator credentials. The external mode is not by itself a
security boundary, and the fixture's read-only login is not a complete Manager
application role.

## Following slices, required before enabling scoped dispatch

### Migration file ownership prerequisite

The next slice makes `/opt/routegate-manager` and its migrations root-owned
(directories 0755, SQL files 0644) on clean installation, both Manager update
paths, and both backup-restore paths. Legacy archive ownership and permissive
modes are normalized. The former `RG_UPDATE_MANAGER_OWNER` override no longer
controls migration ownership. Symlinks, non-regular entries and hard-linked
files are rejected before recursive ownership changes.

An isolated root-only filesystem test uses a real unprivileged identity to
prove readable SQL but denied modification, creation, chmod and directory
replacement, including after restoring an old archive. It also checks linked
entries fail without changing an outside fixture. The existing update suites
remain unchanged. This does not provision SQL roles or enable external mode.

These helpers assume stopped services and a trusted release/backup tree, as do
their existing callers. They are post-install normalization, not an atomic
defence against a concurrently compromised host. Before privileged migration
execution is enabled, staging, path ancestry, credentials, service confinement
and backup trust must be reviewed together. No privileged migration execution
is introduced by this ownership slice.

### Explicit SQL runtime policy (schema 175)

`routegate-manager --create-runtime-role NAME` is a separate administrative
command. It checks the migration manifest, then creates a **new NOLOGIN role**
and its grants in one transaction. It does not migrate, serve HTTP, configure
authentication, demote the legacy owner or change the service environment.
Its database connection needs role-creation and grant authority. Never supply
those credentials to the serving Manager process.

The fixed policy names all 76 current application tables. It grants ordinary
SELECT/INSERT/UPDATE/DELETE and USAGE/SELECT on their owned sequences; migration
history is SELECT-only. There is no TRUNCATE, TRIGGER, sequence UPDATE/setval,
schema/database CREATE, role membership or default grant on future objects.
Existing immutable SQL guards continue to constrain granted DML. Current
PUBLIC function permissions are not rewritten by this command.

Provisioning refuses existing roles, unexpected/missing public relations,
unsupported migration versions and unsafe PUBLIC CREATE/history-write grants.
This slice does not silently repair ambient ACLs or implement reconciliation
of an existing runtime role. Added tables require an explicit policy review;
future dispatch-consumption records must not receive ordinary runtime DML.

Disposable fixtures alone enable LOGIN with generated test credentials. They
exercise built-in RBAC initialization, schema readiness, approved and staged
revocation admission under a real runtime connection, all ten unchanged P0141
mutation denials and their complete durable snapshots. They also deny privilege
escalation, DDL, trigger disabling, TRUNCATE, ledger writes, sequence rewind and
writes to a newly created table, and check failed provisioning leaves no role.

This ordinary DML role is **not a dispatch executor** and is not protection
against all malicious application SQL: it can still write ordinary application
data, including RBAC tables. Protected dispatch authority needs its own reviewed
execution boundary. Installer credential provisioning, role upgrades, full
Manager/browser operation under restricted credentials, restore semantics and
switching the service to external mode remain required. No production role is
created or altered by this repository change alone.

### Full Manager runtime-login rehearsal

Workspace integration now runs two independent disposable PostgreSQL services:
the existing legacy startup, and external migration followed by a real runtime
login. Only the preparation step holds the fixture owner URL. Before launching
the Manager, the workflow replaces its database environment with the restricted
login and sets external migration mode. The existing full browser suite runs
unchanged in each leg, including bootstrap, authentication, server/account CRUD,
routing writes and account lifecycle UI checks. Each leg keeps its 15-minute
limit; no browser assertions are removed.

After the runtime leg, SQL readback through that same login checks persisted
application writes, role attributes, ownership and denied ledger modification,
trigger disabling and owner impersonation. Fixture LOGIN activation and its
test password exist only inside the disposable CI service. This rehearsal does
not yet provision credentials on installed hosts or prove VPN revocation E2E.

### Remaining work

1. Provision separate owner/migrator and runtime roles; never grant owner
   membership or schema CREATE to runtime. Determine explicit DML/function
   grants, including trigger-written audit rows and sequences. Do not broadly
   grant future protected dispatch records via default privileges.
2. Invoke migrations out of process in installation and updates, with credentials
   inaccessible to the Manager service. Set external mode only after migration
   and privilege validation succeed. Inventory ownership of files/service units
   as well as SQL objects: process separation alone is insufficient.
3. Rehearse clean install, existing-owner conversion, interrupted upgrade,
   backup/restore and binary rollback. External startup intentionally rejects a
   database with newer migration versions. Do not silently fall back to owner
   credentials to make rollback appear healthy.
4. Prove representative Manager behavior under the restricted runtime login,
   and deny direct writes to dedicated dispatch authority. Only then implement
   the atomic operation-owned dispatch/claim/completion/recovery transitions.

This slice changes no SQL trigger, installer provisioning, service credential,
or production host. It creates no executable removal job or new evidence ledger.

### External-startup ownership guard

External startup now refuses a session identity that owns the current database,
public schema or any public relation, or can become such an owner through role
membership (including NOINHERIT membership). Superuser, role/database creation,
replication and RLS-bypass roles reachable through membership are also refused.
This SELECT-only check runs before schema readiness and application startup;
errors fail closed. Legacy auto mode and explicit administrative commands retain
separate behavior. A disposable login regression checks owner rejection,
NOINHERIT owner-membership rejection and readiness after membership revocation.

This is not complete effective-privilege validation: explicit ACLs, executable
SECURITY DEFINER functions, later privilege changes and application-writable
RBAC remain separate concerns. Installer credentials, upgrade/restore role
reconciliation and protected dispatch authority are still outstanding.

### Maintenance credential reader (not connected)

A dedicated reader accepts only `/etc/routegate/database-maintenance.env` with
one `ROUTEGATE_MAINTENANCE_DATABASE_URL` entry. It treats content as data,
requires a regular root-owned 0600 file with one link, and refuses missing,
duplicate, unknown or empty entries. It never falls back to `manager.env`.
Root-run isolated tests exercise the real ownership checks; existing update
and restore callers still use their legacy connection and are unchanged.

This helper assumes trusted path ancestry and no concurrent host mutation;
it does not establish a race-free filesystem boundary or validate SQL grants.
Do not connect it to privileged execution until installer provisioning,
credential lifecycle, backup/restore handling and trusted path validation are
implemented together. No maintenance credential is generated by this slice.

### Required runtime grants at external startup

External startup now checks the explicit application's required table DML and
owned-sequence USAGE/SELECT privileges after ownership and migration readiness.
It also requires database CONNECT, public schema USAGE and ledger SELECT. Every
privilege is checked individually: PostgreSQL's comma-separated privilege list
means ANY, not ALL. Missing policy tables fail closed. Checks issue SELECT only,
never restore ACLs or retry with administrative credentials.

This prevents a current migration ledger from admitting a runtime login after
application grants have been lost, including the ACL-loss condition that restore
with `--no-privileges` must handle. Tests revoke each of the 304 required table
privilege bits independently, verify refusal without repair, restore each grant
and verify readiness again under a read-only runtime session. Sequence privilege
loss, a missing policy table and complete application-table ACL loss are covered.
This is an ACL-loss fixture, not a full pg_dump/pg_restore rehearsal.

A ledger-only login can still inspect schema versions through CheckSchema, but
can no longer start the serving Manager in external mode. This is a minimum
privilege availability check, not a complete effective-privilege audit: excessive
ACLs, SECURITY DEFINER functions, RLS and later grant changes remain separate
concerns. Administrative reconciliation, credential provisioning and restore
rehearsal remain required before enabling the installed runtime role.

### Preserved-role restore rehearsal (CI only)

The restored Workspace fixture now enables its disposable runtime LOGIN once,
**before** dumping the database. After `pg_restore --no-privileges`, missing DML
still refuses external startup without repairing grants. The fixture replays
only ACL TOC entries from the custom dump it just created, in one transaction,
then compares public relation/schema ACLs and the migration ledger with their
pre-restore values. It never drops/recreates the role or resets its password.

An owner-only, mode-0600 temporary snapshot compares the complete pg_authid row
(including OID, attributes and password verifier) and role settings before and
after restore and again after the unchanged full browser suite and runtime SQL
readback. Real TCP connections require the original password and reject a wrong
password, avoiding a false proof under trust authentication. The verifier is
never printed or uploaded. ProvisionRuntimeRole retains its new-role-only API.

This rehearses same-cluster identity preservation using a trusted, freshly made
disposable fixture archive. ACL TOC selection is not validation of arbitrary SQL
or backups; it is not an administrative reconciler, excessive-privilege audit,
cross-cluster identity contract or installed-host recovery procedure. A permanent
credential-mode record, binary rollback compatibility, serialized administrative
reconciliation and connected VPN revocation E2E remain outstanding. #545 stays
open.

### Optional durable restore pin: reader and rollback gate only

The rollback preflight now checks `/var/lib/routegate-credential-mode/restore.pin`
when its containing directory exists. Ordinary bundle/Manager backups do not
include this directory. A missing, partial or invalid pin inside an existing
directory refuses recovery rather than falling back to environment detection.
The fixed record contains exactly five ordered lines: `FORMAT_VERSION=1`,
`MODE=external`, and `ENV_SHA256`, `UNIT_SHA256`, `BINARY_SHA256` with lowercase
64-digit hashes. It is parsed as data, never sourced. The record must be a
root-owned 0600 regular file with one link; fixed directory ancestry must be
root-owned, non-symlink and not group/world writable.

Both current and backup env/unit/binary must match the pin byte-for-byte by
SHA-256. Alternate loaded unit fragments, loaded drop-ins and standard on-disk
service/global/prefix drop-in directories are refused (including not-yet-loaded
ones). This is intentionally stricter than a version compatibility range: even
a legitimate changed binary blocks recovery until an administrative transition
protocol is implemented. Existing explicit-mode comparisons remain in place.
Both outer rollback handlers and both file helpers use this preflight after
stopping services and before PostgreSQL/file restoration.

**Installer activation and pin rotation are not implemented.** The unwired
first-publication primitive described below is exercised only by test fixtures. A pin is a trusted administrator's exact-file assertion,
not proof that those files actually implement secure external mode. Activation
must validate runtime credentials, binary behavior, effective systemd execution,
trusted path ancestry and schema/role policy together. It must define atomic
write/fsync and interrupted-conversion behavior, serialized upgrade/rotation and
recovery to a different release. Do not manually activate this experimental pin
on installed hosts as a substitute for that protocol.

This gate assumes the existing privileged caller's trusted host/backup and
serialization boundary. It is not race-free against concurrent privileged file
or unit changes; it does not protect direct startup or older restore tools that
do not contain this gate. Complete removal of the directory by a privileged
actor is not detected. It neither restores ACLs nor executes removal dispatch.
The legacy path remains unchanged when the directory has never been created.
These limitations keep installed credential separation and #545 incomplete.

### First-publication primitive (unwired)

`scripts/rg140_pin_writer.py` now provides `publish_first_pin(parent_fd, payload)`.
It requires root and a root-owned, non-group/world-writable directory descriptor;
the future administrative caller must open and validate the intended `/var/lib`
ancestry. Tests pass disposable directories. There is no CLI, host invocation,
installer activation, automatic hash generation, adoption or rotation.

After strict byte-format validation, exclusive mkdir creates the mode latch;
fsync of its parent precedes writing. The conversion-pending directory described
below is then created and its parent fsynced. A private 0600 pending file is written and
fsynced, renamed within the new directory, then the directory is fsynced. The
latch is never removed on errors. Concurrent first publishers have one winner;
existing partial directories and symlinks refuse publication. A failure of the
last fsync can leave a complete visible pin with uncertain durability: return an
error, keep services stopped and require administrative recovery. Never erase
the latch to retry. These tests inject syscall failures, not physical power loss.

The caller still must validate credentials/schema, binary behavior, effective
systemd configuration and trusted paths, stop services and hold the host update
transaction lock. This primitive only arbitrates first publication, not competing
updates/restores or privileged path replacement. It does not establish the
conversion protocol or authorize pinning arbitrary hashes. Rotation and safe
recovery remain unimplemented; #545 remains open.

### Composed publication/rollback evidence

The real first writer feeds the real reader and both extracted outer rollback
handlers in 28 isolated scenarios (seven outcomes, two handlers, database-mutated
and file-only branches). Parent/intent/file fsync and rename failures retain the
latch and refuse before any mutation. Missing current env also refuses after
successful publication. Every case preserves the publication snapshot and refuses
a second publication. Existing reader/outer-handler positive controls still admit
a matching pin without pending state.

The earlier #662 fixture exposed a gap: a final directory-fsync error returned
an error from the writer but left a complete visible pin accepted by the reader.
The writer now creates `conversion.pending` inside the latch directory and fsyncs
its containing directory **before writing the pin**. Any entry at that name
(including a file or dangling symlink) makes the common rollback gate refuse.
The pending directory remains on success as well as error: publication of hashes
is not completion of credential conversion. A fresh rollback process therefore
refuses even a complete visible pin after the final fsync failure.

There is deliberately no operation that removes this pending marker, no installer
activation and no automatic recovery. Explicit administrative completion/recovery
must be designed with stopped services, the host transaction lock, fresh validation
of credentials/schema/binary/effective systemd and a restart-safe outcome protocol.
Removing a marker after apparent success would create another uncertainty window;
this slice does not solve that protocol. A pre-existing v1 pin without the marker
retains its previous behavior; this does not attest completion of older operations.

Mutation endpoints and systemctl are stubbed; these are composed filesystem/control
flow tests, not physical power-loss, full systemd conversion, PostgreSQL restoration
or VPN revocation E2E. If the initial parent fsync fails, persistence of the latch
itself is uncertain; callers must perform no credential/configuration mutations
before establishing durable intent. Privileged deletion, concurrent host mutation,
direct startup and old rollback tools remain outside this gate's protection.
#545 remains open.

### Normal Manager startup and interrupted conversion

The current Manager binary checks the fixed host latch path before its first
PostgreSQL connection in normal startup. A pending entry of any type (including
a dangling symlink), an invalid latch directory, an incomplete directory without
a regular restore pin, or an inspection error withholds startup. Both auto and
external migration modes use this path. A host without the latch starts as before.

The service runs as `routegate`. For a root-owned 0700 latch it cannot reliably
determine whether a child exists, so even a pin-only directory with these
permissions withholds startup. A traversable legacy pin-only layout passes
this particular check; rollback retains its separate, stricter pin verification.
A pin is not a completion record. Do not change permissions or delete state to
clear a startup failure. The writer still has no production activation path.

This check covers the current binary at startup. It assumes trusted host
ancestors and a stopped Manager during administrative conversion. It does not
serialize concurrent publication with startup, stop an already running process,
or fence explicit migration/provisioning commands or older binaries.

### First publication versus native Manager execution

On a native Linux host, the offline first-pin writer pins the approved Manager
executable inode, verifies root ownership and safe permissions, then opens that
inode read/write without truncation. The writer reads the binary to verify the
SHA-256 from the pin payload. It never writes executable bytes. Linux returns
ETXTBSY if the executable is in use, and refuses exec of the same inode while
the descriptor is open. This spans the first mkdir through the final fsync and
exception cleanup. After release, the current Manager checks the durable latch
as described above. A write-open refusal leaves the latch untouched.

This relies on a local filesystem that enforces Linux executable/write
exclusion. The approved installed path and inode must remain stable under the
host transaction lock. A read-only or immutable executable can refuse even
offline publication. A copied/replaced binary, a different mount view or
direct SQL access requires separate control. The primitive remains unwired;
before activation the administrative caller must establish service quiescence
and validate credentials, effective units, paths and schema. Existing pins
remain untouched by a refused publication.

### Detecting live Managers after binary replacement

Immediately before creating the latch, the writer resolves the installed
service account `routegate` and inspects numeric process directories in host
procfs. Any process leader with that real, effective, saved or filesystem UID
withholds publication. This catches renamed copies and running executables
unlinked by a package replacement, without relying on a process name, binary
digest, command line or database URL. Any other process using the account
also blocks this offline operation. A missing account, unreadable or malformed
status, restricted UID mapping or a hidepid mount withholds publication.

This is a point-in-time observation in the host PID and user namespaces.
A privileged caller must establish that host view, hold the update lock,
stop the service and exclude new launches from alternate executables before
credential mutation. An empty scan cannot establish these preconditions.
Processes running solely under another UID, external database clients and
new launches after enumeration require independent controls. The same-inode
kernel exclusion and durable pending marker retain their separate roles.

### Process recheck behind durable intent

First publication keeps the early idle-account check to refuse an already
visible process before creating host state. After creating and fsyncing the
latch and pending marker it checks the account again, before creating even the
temporary pin. A live service process that passed the first scan and entered
database startup prevents publication. A second scan failure retains the
durable pending marker for explicit recovery. The writer never clears it.

For normal Managers with the startup check from #677 in the same filesystem
view, the visible latch withholds a later copy before database initialization.
This does not undo work performed by a transient process before the recheck.
Older binaries that ignore the latch can still start after enumeration.
Administrative control of those launch paths and database sessions remains a
prerequisite for actual credential changes; the writer remains offline and unwired.


### Old-login session rehearsal (disposable PostgreSQL only)

The dedicated CI test `TestCredentialConversionSessionFence` exercises two fresh
roles, without application grants or changes to an existing role. It checks that
NOLOGIN must commit before new connections are refused; rollback leaves LOGIN
available, and already authenticated sessions can continue SQL after the commit.
The committed prohibition survives replacement of the administrative connection.

The rehearsal selects old-login backends by role OID, without a database filter,
and uses `pg_terminate_backend(pid, 5000)` with a positive timeout. Every exit must
be confirmed and a fresh activity read must report zero matching sessions. Both
old connections must fail further SQL, their transaction advisory lock must be
released, and the unrelated role must retain both an existing session and fresh
login. The fixture never automatically restores LOGIN after an error.

A deliberate counterexample grants the other login membership in the NOLOGIN
role: SET ROLE still succeeds while session_user remains the other login. Thus
NOLOGIN plus a usesysid scan is not a complete authority fence. Before installer
activation, inventory alternate credentials, memberships (including indirect
ones), owner/superuser authority, prepared transactions and connections already
in authentication. A single empty activity scan does not exclude those paths.
Administrative concurrency, restart-safe recovery and credential replacement
remain separate work. This test is not a production conversion command and does
not connect the pin writer to PostgreSQL. #545 remains open.

PostgreSQL semantics: https://www.postgresql.org/docs/16/functions-admin.html
and https://www.postgresql.org/docs/16/sql-alterrole.html.


### Read-only incoming-membership preflight (unwired)

`RequireNoCredentialConversionMembers` checks a caller-supplied role OID/name
pair in one SELECT against qualified PostgreSQL catalogs. A missing or replaced
identity, inspection error or any incoming member withholds admission. Recursive
UNION follows direct and indirect members without filtering on LOGIN, INHERIT,
SET or ADMIN options. Even disabled-option and NOLOGIN memberships are refused
conservatively; the helper does not revoke grants or alter roles. Outgoing
membership alone is not an incoming path and does not fail this narrow check.

PostgreSQL 16 integration tests execute the helper inside READ ONLY transactions
and compare role attributes and memberships before/after each decision. They
cover independent roles, outgoing membership, direct SET-only, INHERIT-only,
ADMIN-only and disabled-option grants, and a real login that can SET ROLE through
a NOLOGIN/NOINHERIT intermediate. Removing that incoming edge admits the narrow
check again. Missing, mismatched and recreated identity pairs and cancelled
inspection must fail. The previous session-drain rehearsal remains unchanged.

This is a snapshot, not a durable database fence. No installer or conversion
caller is connected. A concurrent administrator can grant membership after the
check; already-assumed identities, superuser access, direct object ACLs, SECURITY
DEFINER code, prepared transactions and authentication already in progress are
not excluded. The caller still needs an explicit administrative concurrency and
recovery contract before credential mutation. A nil error from this helper alone
must never authorize conversion or mark it completed. #545 remains open.

Membership options: https://www.postgresql.org/docs/16/role-membership.html.


### Prepared-transaction preflight (unwired)

`RequireNoCredentialConversionPrepared` validates the old role and Manager
database OID/name pairs in one SELECT. It refuses old-role prepared transactions
anywhere in the cluster and prepared transactions of any owner in the pinned
database. An alternate login can prepare writes against the same tables, and
the catalog does not establish which application data a transaction changed.
Missing/mismatched identity or query failure withholds admission. The helper never
issues COMMIT/ROLLBACK PREPARED, changes LOGIN or prints transaction GIDs.
The external coordinator/administrator must decide how to resolve pending work.

The PostgreSQL 16 backend CI service enables eight prepared-transaction slots
before testing; no installed PostgreSQL configuration is changed. The test
creates two disposable databases and two roles. It prepares writes in both
bases as the target role, terminates its backends, sets NOLOGIN and verifies zero
sessions. The preflight must still refuse from a third database. READ ONLY
transactions and before/after snapshots prove that refusals do not resolve work.

Explicit fixture COMMIT PREPARED in one database leaves the gate blocked by the
other; explicit ROLLBACK PREPARED of the last target transaction clears only this
gate. Row readback proves the commit/rollback effects. An unrelated prepared
transaction in another database remains throughout and is removed only by
fixture teardown. An alternate owner's prepared write in the pinned database
blocks until explicitly rolled back. Invalid role and database identities and
cancellation also refuse. Teardown knows only its randomly named fixture objects.

This snapshot does not exclude concurrent preparation, work under alternate
identities outside the pinned database (including changes to shared catalogs),
direct ACLs, SECURITY DEFINER execution or administrative changes. Existing
membership/session gates and host intent remain separate primitives. Installer
activation, serialized admission, restart-safe outcome handling and VPN
revocation E2E are still incomplete.

PostgreSQL semantics: https://www.postgresql.org/docs/16/sql-prepare-transaction.html.
