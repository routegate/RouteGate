# RG-140 consolidation ledger — v0.2.0 (review-only)

**Canonical consolidation candidate:** `integration/rg140-v020-consolidated-20261116`.  
**Base:** `main` at `edad1229b87babba5fd9513e220e1291a5f934ff`.  
**Preserved source snapshot:** Draft [#789](https://github.com/routegate/RouteGate/pull/789) at `81c0c9a9c34bfcbac384444c67b9563d51ddeb5e`.  
**Alternative, not to merge blindly:** Draft [#811](https://github.com/routegate/RouteGate/pull/811) at `63f6ec7d5dba48568e81e4d6425927c6684ee31d`.  
**Issue/acceptance:** [#545](https://github.com/routegate/RouteGate/issues/545) for verified runtime revocation; [#509](https://github.com/routegate/RouteGate/issues/509) and [#538](https://github.com/routegate/RouteGate/issues/538) are related, not interchangeable.

**Status: NOT RELEASE-READY / NO-GO. No production deployments, schema writes or Agent credential removal.**

## Completion audit — 19 November 2026

This section supersedes older status/count/next-step notes below; those notes
remain provenance, not a queue of new architecture work. Baseline inspected:
`a31fdd3a2f8ecf0a354cc018f8676ee19a1ef354` (107 commits, 251 changed files,
79 non-test Go files). All six ordinary CI jobs except Manager-to-Reality
passed in run `44399731535`; Workspace `44399731980` and Docs `44399731585`
also passed. The failing bootstrap reached `config_apply/witness_unavailable`.

### First blocker: accidental recovery opt-in after legacy installation

The missing running witness is the immediate rejection, **not the root reason
an ordinary new node entered experimental recovery**. After mandatory shared
admission was added to legacy tasks, successful `vpn_core_install` left an
idle directory containing `runtime.lock`. `prepareRuntimeMutationHeartbeat`
used directory existence to advertise `runtimeMutationFencingV1`; Manager
persisted that capability, and the next ordinary first apply required an
already running sing-box. Default bootstrap does not set the experimental flag.

The narrow fix reuses `RuntimeMutationIdleForPlatformUpdate` in heartbeat
capability selection. A completed shared lease is not enrollment. Explicit
opt-in, prior Manager acknowledgement, unresolved markers, unknown files,
unsafe state and lock contention retain conservative negotiation. Actual
dispatch still acquires the same durable lease. No witness validation, Manager
sticky policy, lock, journal, dispatcher or offline enrollment code is bypassed
or replaced. Already stuck nodes are **not** automatically unlocked/reset.

The new HTTP-heartbeat regression reproduces install completion -> capability
exchange -> first-apply admission; it fails with the old predicate and passes
with the fix. Negative cases cover retained/held install intent, unknown state,
explicit opt-in and a Manager that already persisted fencing. Existing missing,
corrupt and untrusted witness tests remain unchanged. Real E2E must separately
confirm the complete bootstrap; callback admission alone is not VPN traffic.

### Existing work and remaining transfers

| Area / source | Verified state and disposition |
| --- | --- |
| #789 snapshot | Preserved as the consolidation's initial identical tree; experimental source ancestry was already excluded by that one-parent snapshot. Later scope cuts are documented below. |
| Canonical SQL | One ordinal each at 160–170; 160 is unchanged. Source comparison confirms legacy #713 160 -> current 161 and 173 -> 168 have identical executable SQL. 174 -> 169 and 175 -> 170 are adapted with current human super-admin authority. No 175/176 collision was imported. |
| #647 | External runtime owner/membership refusal is already adapted in the shared `CheckExternalMigrationRuntimeRole`; do not port a second guard. |
| #639 / #652 | Historical schema-175 grant creation/required-grant checks are not ported. Their table list is incompatible with canonical 170. Reuse the implementation pattern only after adapting the existing table review matrix. |
| #713 / #703 | Held dispatch/transport, terminal evidence/commit, retirement and failed-result handling remain reuse sources. Current production router and Runner do not mount credential removal. The current two-user Reality contract test proves local effect, not that complete product path. |
| #811 | Session-authorized preparation was selectively ported. Alternative 161–165 schema and dispatch identity path must not be merged wholesale. |
| Shared admission | Agent and detached update worker now both call `BeginRuntimeMutation`; the earlier ownership review's claim that the worker takes no lock is stale. Real worker/Agent restart and mixed-version compatibility remain acceptance gaps. |
| Duplicates | Hash scan found no byte-identical non-test Go files involving this PR. The two trust-boundary validators are intentional. This is not a claim of an exhaustive semantic duplicate audit. |

### Finite completion gates (no new parallel subsystems)

1. **Bootstrap regression:** land the narrow capability fix and obtain green
   generated-command -> Agent -> first apply -> real Reality E2E. Never clear
   old ambiguous intents to manufacture success.
2. **Canonical dispatch prerequisites:** adapt #639/#652 role/grant wiring to
   schema 170 using the existing external migrator and guard. Verify installer,
   update and restore ownership boundaries in disposable environments before
   adapting #713 protected dispatch tables; no legacy migration cherry-pick.
3. **Complete the existing revocation path:** adapt #713/#703 held issuance,
   authenticated claim/result, independent terminal evidence, atomic terminal
   state and retired-credential fence; connect one human-authorized production
   entry point and one Agent path. Acceptance is the issue #545 two-account
   test through those actua…1611 tokens truncated…e erased.

## Working protocol to prevent another fork

1. Before implementing a feature, search **both source tips** and this ledger for an existing function and proven acceptance gap.
2. State the exact failing test or missing contract, choose a minimal change and record it here.
3. Keep development on the one consolidation branch or a small review branch *based directly on it*.
4. Never equate a green isolated Draft with a release-ready RG-140 implementation.
5. Do not open a new alternative migration lineage.

## Consolidation cut — isolated RG-140 from release/installer/site work

- Exact reviewed starting point: #812 branch commit `17e710b35b56261bcdcf6bbc762460df878fbfa7`.
- Reverted **29 changed unrelated paths** to their `main` blobs or removed
  the files that did not exist in `main`: website Pages/snapshot, release
  promotion and tag enforcement, public quick start, Grafana/Hybrid installer,
  release candidate scripts, installer documentation and published website.
  These changes remain intact in [source #789](https://github.com/routegate/RouteGate/pull/789)
  and the dedicated installer/release branch history; this is **scope separation,
  not deletion of user work**.
- `.github/workflows/ci.yml` now retains **all RG-140-specific Backend and Agent
  test steps** from the integration source but uses the current `main` installer,
  bootstrap, frontend and website CI jobs. This deliberately removes release
  publication rehearsal and disposable native Grafana/Hybrid jobs from this
  RG-140 PR; they are tested in their own release candidate line.
- Retained `deploy/nginx/routegate.conf.example` because subscription bearer
  protection is related to RG-140 #509/#538; verify its privacy and continuity
  effect independently before merge.
- Do NOT claim the entire branch is release-ready solely from this scope cut.
  Validate the exact new head. Issue #545 two-account runtime-revocation end-to-end
  acceptance remains separate from healthy ordinary VPN traffic tests.

## Verified Manager → Agent delivery gap and historical success replay

At consolidated commit `3a175d301a22521a86e361d957b27776caa99277`, `backend/internal/http/router.go` registers generic Agent task poll/result endpoints, **no credential-revocation operator endpoint**. `backend/internal/agents/repository.go::ClaimNextConfigTask` only claims existing generic `config_apply_jobs` and supplies `rendered_config`/hash; the current `agent/internal/heartbeat/runner.go::dispatchTaskUnfenced` switch does **not** handle `TaskKindCredentialRemoval`. The scoped `CredentialRemovalExecutor.Execute` is dormant in production. The target #545 behavior remains **unimplemented end to end**; ordinary Manager→Reality traffic and the isolated Agent two-user Reality systemd test are **not** proof of live credential revocation.

Do not wire the dormant executor to a generic `config_apply` job: the approved DB reservation, one-use operator confirmation, exact task identity, Agent acknowledgement, final runtime evidence, and fail-closed pending UI are not yet a single reviewed execution boundary. Plan one authorized end-to-end workflow, not a second journal.

**Receipt correction:** replay of a historical `succeeded` removal receipt now uses the *same strictly increasing service generation proof* as initial completion; non-advancing systemd monotonic timestamps trigger `recovery_required` with no re-execution. Includes a concrete persisted-receipt replay regression, not a new task protocol.


## Historical full Manager–Agent Reality E2E discovered (do not rewrite it)

During reconciliation, found independent October Draft [#703](https://github.com/routegate/RouteGate/pull/703),
HEAD `b32ab7f92c0cea65e256b3fbe0e650d3f30757ae`, stacked on [#697](https://github.com/routegate/RouteGate/pull/697).
Its [exact-head CI](https://github.com/routegate/RouteGate/actions/runs/42183503313) and Workspace Integration were **SUCCESS**.
The old CI explicitly builds Agent and Manager test binaries and runs
`TestCredentialRemovalManagerAgentReality` with a disposable PostgreSQL database,
isolated systemd and pinned sing-box 1.12.8. The test proves the old isolated
stack could terminate an existing VLESS/Reality stream, reject the removed UUID,
and preserve the second account. **It does not prove the current production
`router.go` mounts that operation.** In #703 the Agent credential-removal
claim/result endpoints are **explicitly mounted only into an isolated test
`http.ServeMux`** via `agents.Handler.MountHeldRemoval`.

Reusable, exact source files:
- `backend/internal/db/credential_removal_manager_reality_integration_test.go`
- `agent/internal/tasks/credential_removal_manager_reality_test.go`
- `agent/internal/tasks/credential_removal_manager_child_test.go`
- `backend/internal/agents/removal_transport.go` (old isolated transport)
- `agent/internal/client/held_removal.go` (old task client)

**Why not cherry-pick #703:** The legacy stack has
`000160_credential_revocation_preparations.up.sql`, but current `main`
uses schema **160 for traffic checkpoints**. It then introduces unrelated
recovery schemas 161–172 and dispatch/transport schemas 173–177, while our
canonical #812 lineage already owns 161–167 for different objects. Its
`ExecuteHeld` and privileged dispatch contracts are not interchangeable
with the #812 fenced Agent or task claims. Preserve source #703 and #713
until source-level parity and authorization review are complete.

**Reconciliation rule:** Use that **already validated test fixture** as the
reference for eventual modern isolated two-account E2E, and port/adapt only
the necessary code after defining one Manager human-authorization → immutable
task → authenticated Agent claim → exact result/evidence → status transition
boundary. Any additional approved schema uses **168+** in #812's canonical
line; never import legacy migration numbers or expose unreviewed
`MountHeldRemoval` routes in production. Keep operator confirmation and
lost-acknowledgement behavior fail-closed.

## Modern cross-module compatibility gate (no dispatch)

An opt-in CI test exports **real PostgreSQL-backed Manager** `Service.Prepare`
candidate bytes from an isolated applied baseline into a mode-0600 ephemeral file,
then invokes **the actual Agent** `verifyRemovalRuntimeDelta` and
`CredentialRemovalExecutor.Execute` with a fake local service controller.
It checks exact post-removal runtime hash, rejection before mutation on tampered
input and read-only replay. No production Agent task is claimed, no human
execution approval is fabricated by Manager, and **no real sing-box restart or
network session termination is claimed by this gate**.

This fake-service contract test is intentionally narrower than the new
real-systemd Manager-prepared Reality test documented below. The authenticated
production delivery/result lifecycle still needs a separately reviewed 168+
schema adaptation; no legacy SQL was imported. The ephemeral fixtures contain
test Reality private material and must never be checked into Git or uploaded
as CI artifacts.


## Manager-prepared two-user sing-box/Reality execution verified

The historical two-account traffic acceptance from [#703](https://github.com/routegate/RouteGate/pull/703) /
[#713](https://github.com/routegate/RouteGate/pull/713) has now been **adapted**
to the **current** Manager PostgreSQL `Service.Prepare` output and existing
Agent `CredentialRemovalExecutor`, with no conflicting SQL or production
route imported.

- Manager fixture: `backend/internal/db/credential_revocation_agent_contract_export_test.go`.
  Before rendering the applied baseline, configure a disposable `localhost`
  Reality SNI and temporary listener port. The candidate comes from the
  actual `vpn_credential_revocations` row, not handwritten Agent JSON.
- Live fixture: `agent/internal/tasks/credential_removal_manager_reality_contract_test.go`.
  Run pinned sing-box 1.12.8 as a **new isolated systemd unit**, exercise two
  independent VLESS credentials over loopback, execute the Manager-prepared
  removal, check both pre-existing streams end on the shared process restart,
  check second account reconnects, and verify durable report replay.
- Strict denial proof: ported the legacy #703 negative probe (HTTP `GotConn`
  for the *local* proxy, reject network timeouts, reject any request arriving
  at the test HTTP origin, reject successful/non-5xx HTTP responses). This
  prevents proxy death or test-network hangs from being counted as revoked-UUID
  rejection.
- CI: the disposable PostgreSQL/backend job in `.github/workflows/ci.yml`
  generates a private mode-0600 candidate fixture, downloads/checksums the
  pinned sing-box binary and runs the test with sudo **only on the ephemeral
  GitHub Actions VM**. Fixture is deleted, no artifacts/credentials published.
  The dedicated live-Reality test step is **SUCCESS** at
  [run 43983861513](https://github.com/routegate/RouteGate/actions/runs/43983861513).

**Precisely what this proves:** the current Manager's candidate bytes and
the current Agent executor can revoke a targeted UUID on real sing-box/Reality
while preserving the other account's fresh access, in isolation.

**Precisely what it does NOT prove:** this test directly invokes the Agent
executor with a synthetic task ID. It does **not** prove a live human
confirmation endpoint, immutable authorized dispatch, Agent HTTP claim,
durable acknowledged result, separate independently verified terminal
observation, session/account state transition or subscription token retirement.
The `vpn_credential_revocations` prepared ledger retains its fail-closed
reservation, and **RG-140 #545 remains NO-GO**.

**Next implementation boundary:** adapt the *already reviewed* isolated
claim/result and confirmation contracts from #703/#713 to the canonical
161–169 history. Choose collision-free new **170+** migration(s), with
exact Agent identity, one-use dispatch, transport loss/unknown outcome
reconciliation and independently verified terminal proof; the human consent
ledger is now preserved separately but is **not yet executable**.
Do not simply mount the legacy test-only HTTP endpoints or route a removal
task through the generic config-apply queue.


## Canonical 168–169: immutable proposed task IDs and human approval

These are **selective ports of the original reviewed #713 code**, not
another parallel authority/transport implementation. The legacy source
migration 173 is now `000168_credential_removal_dispatch_bindings` and legacy
174 is now `000169_credential_removal_dispatch_authorizations`. They follow
our existing 161–167 schema. The original 160 preparation SQL and the current
161 SQL were compared: identical executable statements, with a comment-only
difference. **Old 173/174 names must never be cherry-picked in addition.**

- `backend/internal/revocations/prepared_integrity.go` independently
  rechecks in a read-only repeatable-read snapshot that the prepared frozen
  candidate and applied node/account/Agent generation/hashes still match.
  This **does not** issue authorization.
- `removal_job_binding.go` uses server → preparation lock order and
  `vpn_credential_revocation_dispatch_bindings` to assign **stable, never
  yet issued** candidate version/job UUIDs. A concurrent retry returns
  the same IDs, while direct SQL changes, forged hashes/generation and
  destructive downgrade fail closed. Original prepared reservation continues
  to reject generic config-apply jobs.
- `removal_dispatch_approval.go` records **explicit fresh human consent**
  bound to exactly those IDs, the original candidate hashes, Agent generation,
  one user/session and an expiry no later than two minutes. It creates no
  actual config version, config apply job, Agent task or result.
- `removal_dispatch_actor.go` and SQL migration 169 **tighten legacy #713
  `admin` permission** to match our current authorized preparation: an active
  human **`super_admin`**, live and unrevoked session, with all three
  permissions `system:manage`, `configs:apply` and
  `vpn_users:disable`. These grants are rechecked **under database locks**;
  caller-supplied identity is not trusted. PostgreSQL also independently
  refuses direct INSERT of an ordinary admin or incomplete permission set.
- Both DOWN migrations refuse to discard a nonempty durable history.

Tests: `backend/internal/db/credential_revocation_dispatch_binding_integration_test.go`
and `credential_revocation_dispatch_approval_integration_test.go`, run
early with real PostgreSQL in the existing Backend CI job. Targeted
migration 168, migration 169, and direct-SQL privilege-bypass steps were
**SUCCESS** on [run 43986030449](https://github.com/routegate/RouteGate/actions/runs/43986030449)
at exact code HEAD `4e716db8622959be6a45a790000c8ccdc9f8376d`; the
full CI run may still be in progress and must be inspected separately.

**No change to the release gate:** a consent row can neither authorize itself
nor prove an Agent executed anything. No HTTP endpoint/Runner handler consumes
this row. A future reviewed **170+** stage must atomically consume a *fresh*
human decision under its original account/Agent/node locks and admission gate,
create a narrowly held work item and account for connection-loss/unknown
outcomes. Only after authenticated Agent delivery and independently
verified real traffic evidence may a terminal PostgreSQL/UX/subscription
transition be considered. Until then RG-140 #545 remains **NO-GO**.

**Branch cleanup:** keep #703/#713/#811 and related draft sources until
their unique code and tests are accounted for. Never delete an unmerged
branch solely because this PR currently builds; no `main` or VPS changes.


## Canonical migration 170: non-executable intent staging

Selective port of the reviewed #713 legacy migration 175 plus transaction-bound
admission gate: `000170_credential_removal_dispatch_stages`.
A stage is **NOT** a config version, executable job, Agent claim or proof of
revoke. Under the original server → preparation → Agent → user/session locks,
it rechecks applied baseline, original Agent credential generation, frozen
168 UUID/hash binding and fresh 169 nonce/approval. Both Go and SQL require
current active human `super_admin` with `system:manage`, `configs:apply`,
`vpn_users:disable` (legacy `admin` discarded). Ordinary config_apply
remains blocked by P0141; history is immutable and downgrade is non-destructive.
The adapted old PostgreSQL tests check concurrent staging, forged job IDs,
stale Agent, revoked human session, cancellation and lock-wait invalidation.

**No production dispatcher or HTTP route is mounted; no task, version,
subscription or VPN account is changed.** The actual trusted held-dispatch
and authenticated result path remain for a reviewed, separate **171+** stage.
RG-140 v0.2.0 remains NO-GO.

- Schema-170 direct-SQL regression verifies that even a stored, originally
  valid approval cannot be staged after the original super_admin's live role
  is revoked: PostgreSQL refuses the INSERT (P0154) independently of the
  stale browser role claims, and no stage is persisted.


## Independent signed removal intent preflight (no migration / no issue)

Selected read-only legacy #713 `RemovalDispatchDecision` and Ed25519 verifier
are now adapted to our 168–170 PostgreSQL authorization/staging sources.
`backend/internal/revocations/removal_dispatch_authority.go` independently
matches the original Manager-side human approval, exact account/node, Agent
generation, version/job IDs, baseline and candidate hashes, expiry, audience
and nonce against a signature from a **separately supplied public verifier
key**. It uses current `authenticatedRevocationActor` plus the locked live
`super_admin` and three-grant validation from schema 169/170.

The private signing key is generated **only in the isolated test**, and this
code does not provision an actual independent signer/coordinator or elevate the
Manager runtime to signing authority. No HTTP route or task is introduced.
The legacy #713 held-issued version/job path requires additional isolation
of dedicated SQL roles; it remains unported, pending security review.

The targeted PostgreSQL regression denies wrong keys, unsigned decisions,
tampering with every bound field, and session revocation after a valid
signature. It checks no config version/job/stage is allocated by verification.
This remains a **non-executable preflight**; actual dispatch/receipt/terminal
proof still NO-GO.


## Blocking owner/privilege issue before new held-dispatch schema 171

`install.sh` provisions the `routegate` login as database owner and
`app.Start` runs migrations using the same runtime `db.Connect` pool.
Thus historical #713 migration 176's `REVOKE FROM PUBLIC` cannot isolate
its held-removal table from the ordinary Manager **which is its owner**.
Do **not** port it as new 171 until a split migration-owner/runtime-role
and independent coordinator are implemented. See
`docs/development/rg140-held-dispatch-role-isolation.md`.

New early PostgreSQL negative CI guard
`TestCredentialRemovalHeldDispatchOwnerIsolationGate` fails if a future
`routegate_dispatch` namespace is still owned by, or USAGE-granted to,
the ordinary Manager runtime role. The test is not a grant of authority or
a substitute for complete DB-role isolation. No production permissions,
credentials, nodes or application startup behavior changed.

- Added fail-closed Manager startup check `CheckHeldDispatchRuntimeIsolation` and
  disposable PostgreSQL regression proving a schema owner bypasses PUBLIC
  revocation. The check runs before Manager services and is inactive on
  canonical 170 where private held dispatch schema is absent.


### Two-login role-isolation rehearsal (no production cutover)

Added a gated disposable PostgreSQL `routegate_ci` test with independent
schema-owner NOLOGIN and Manager-runtime LOGIN (NOINHERIT / NOCREATEROLE).
An actual second PG session proves public read with private schema denial,
forged receipt denial, no SET ROLE to owner, then deliberately grants owner
membership to prove the startup guard fails even for non-inherited roles.
The test drops the disposable test schema and roles. Not a production migration
or replacement runtime connection. Future migration/installer work still NO-GO.


### Explicit read-only external migration mode (not deployed)

Added an opt-in `ROUTEGATE_DATABASE_MIGRATION_MODE=external` Manager path:
verify complete `schema_migrations` catalog with SELECT-only and fail
startup on any missing/foreign migration instead of running DDL.
Default `startup` is unchanged. Added source-only
`backend/cmd/routegate-migrate`: separate migrator and runtime database
URLs are required, identities cannot match or inherit the migration owner,
and post-migration catalog/held-dispatch isolation is revalidated through
the runtime login. Not installed, no migration 171, no production cutover.

## Schema-170 role-policy inventory and shared authority guard (17 Nov 2026)

- Existing `CheckExternalMigrationRuntimeRole` now supplies the **one shared**
  ownership/administrator test when the future private `routegate_dispatch`
  namespace exists. `CheckHeldDispatchRuntimeIsolation` reuses it and adds
  only the namespace-specific `USAGE`/`CREATE` ACL denial. Previously the
  two checks could diverge (e.g. REPLICATION or public-relation ownership).
  Two-login PostgreSQL regressions cover both escalations and recovery when
  the extra authority is removed. Reuse, not a new runtime role subsystem.
- Historical [#639](https://github.com/routegate/RouteGate/pull/639)
  and [#652](https://github.com/routegate/RouteGate/pull/652) remain the
  sources for grant issuance and missing-grant detection **patterns**, but
  their static schema-175 table list is **not** valid for current schema 170.
  A read-only scan of all 95 current `.up.sql` migration files reconstructed
  **74 candidate public base table names** versus 76 in #639:
  11 canonical tables are absent from #639 (one traffic checkpoint and
  ten `vpn_runtime_*` recovery/checkpoint relations), while 13 legacy
  `runtime_recovery_*` relations are absent from canonical migrations.
  This is a source-level inventory, **not** yet a successful live catalog
  attestation or an authorization to grant DML to every table.
- `TestRG140Schema170PublicTableInventory` now compares the names from
  migrations with the actual `pg_catalog.pg_class` public base-table
  catalog on disposable PostgreSQL after applying schema 170; it deliberately
  excludes the special read-only `schema_migrations` journal and performs
  **no privilege changes**. It is called in the **existing** external
  migration preflight CI step, not through a parallel grant-policy system.
  Check the current exact-HEAD test result before treating the inventory
  as verified.
- **Next safe action:** distinguish ordinary Manager DML from privileged
  issuance-only tables and verify every allowed table/sequence operation under
  a truly restricted LOGIN. Adapt #639/#652 only after that reviewed
  least-privilege matrix exists; never use `GRANT ALL ON ALL TABLES`, copy
  schema-175 names wholesale, switch live credentials, import legacy schema
  176 as canonical 171, or claim an executable Agent job.
- Work is exclusively in Draft #812. `main`, existing VPS, installed
  Manager configuration, Git history and legacy branch/PR retention are
  unchanged. **RG-140 #545 / v0.2.0 remains NO-GO.**
