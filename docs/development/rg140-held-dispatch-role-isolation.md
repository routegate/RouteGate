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
