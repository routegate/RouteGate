# RG-140 consolidation ledger — v0.2.0 (review-only)

**Canonical consolidation candidate:** `integration/rg140-v020-consolidated-20261116`.  
**Base:** `main` at `edad1229b87babba5fd9513e220e1291a5f934ff`.  
**Preserved source snapshot:** Draft [#789](https://github.com/routegate/RouteGate/pull/789) at `81c0c9a9c34bfcbac384444c67b9563d51ddeb5e`.  
**Alternative, not to merge blindly:** Draft [#811](https://github.com/routegate/RouteGate/pull/811) at `63f6ec7d5dba48568e81e4d6425927c6684ee31d`.  
**Issue/acceptance:** [#545](https://github.com/routegate/RouteGate/issues/545) for verified runtime revocation; [#509](https://github.com/routegate/RouteGate/issues/509) and [#538](https://github.com/routegate/RouteGate/issues/538) are related, not interchangeable.

**Status: NOT RELEASE-READY / NO-GO. No production deployments, schema writes or Agent credential removal.**

## What has actually been consolidated

1. On the consolidation branch, the first commit `de84758c0419df76c59932b48a68edcb3cecde37` has **one parent, current `main`**, and uses the *identical Git tree SHA* `2301ba6c136c2c05d2de16fce16350ba38701b70` from #789. Thus its code is initially identical to the already-tested large integration preview, but its **461 experimental commits are not ancestors** of this new branch.
2. Next, ported **only** the new *unmounted session-authorized preparation adapter* from the #811 lineage, with its unit and PostgreSQL integration tests. This supplies authenticated human super-admin + explicit shared-process-impact acknowledgement instead of trusting a caller-supplied audit actor. It adds **no SQL migration, API endpoint, Agent task or job**.

This is **history consolidation and the first narrow reconciliation**, not the completion of the end-to-end revocation workflow. The inherited #789 snapshot includes Hybrid/Grafana and website/release changes, which must be scoped/split on review **before merging**; it must not be treated as a small RG-140-only PR.

## Canonical migration numbering decision — provisional

| Ordinal | Consolidation candidate (preserved from #789) |
| --- | --- |
| 161 | `000161_credential_revocation_preparations` |
| 162 | `000162_runtime_recovery_reservations` |
| 163 | `000163_runtime_recovery_challenge_ledger` |
| 164 | `000164_runtime_recovery_attempt_ledger` |
| 165 | `000165_runtime_recovery_observation_nonce` |
| 166 | `000166_runtime_pre_dispatch_checkpoint` |
| 167 | `000167_runtime_recovery_enrollment_preparation` |

The alternate #811 branch implements **different SQL at 161** and assigns **unrelated operations to 162–165**. These two histories are **not** safe to merge, concatenate, rename or cherry-pick. Any reviewed additional durable object must be integrated as a separate migration **168+**, subject to verifying existing production and test database schema state and upgrade safety. Never assume unmerged migration numbers were executed on production.

## Deduplication decisions

| Source | Current decision | Reason |
| --- | --- | --- |
| #789 / #784 / #783 | Keep as immutable reference until final review | Existing recovery, Agent, journal, Manager evidence, tests and 161–167 schema |
| #737 | Keep pending differential review | Disposable two-user VLESS/Reality test and later Agent hardening may not be identical |
| #713 | Keep pending differential review | Earlier distinct recovery history; unverified unique hardening |
| #811 (and #806/#808/#809/#810) | Do **not** merge full stack | Alternative SQL and unmounted duplicate/parallel dispatch model |
| #811 authorized preparation + tests | **Ported** | Isolated, no schema conflict, typed session identity; still dormant |
| #811 schema-162..165 confirmation/claim/task reservations | **Not ported** | Collide with canonical migration ordinals and recovery architecture |
| #811 identity parser / Agent token matcher | Pending protocol gap analysis | Identity check alone is not authenticated execution or receipt |
| #811 claim-readback/concurrency tests | Pending adaptation | Incompatible alternate dispatch tables; avoid tests of wrong contract |
| #745/#746 | Preserve through #789 | Their changes are ancestors of the consolidated source snapshot |

A branch being an ancestor of a survivor is evidence that its **commits** are preserved in that survivor, **not** proof that its PR can safely be closed. Check reviews, external consumers, unique revisions and dependent PR bases first. Avoid deleting canary/ops branches without verifying whether they hold operational recovery history.

## Non-negotiable acceptance before changing `main`

- [ ] Code/ADR diff: identify which RG-140 actions are really mounted versus intentionally dormant. No completed-via-HTTP-200 inference.
- [ ] Verify all authorization, replay and result evidence bindings to current Agent identity and candidate version.
- [ ] Real PostgreSQL migration upgrade and rollback/downgrade history: exact ordinal 160→167; never re-use 161–165 from #811.
- [ ] Check Agent's durable, one-shot failure/recovery semantics across restart, stale generation, lost reply and simultaneous tasks.
- [ ] Disposable two-or-more-user sing-box VLESS/Reality run, including old-session termination and unrelated-user preservation.
- [ ] Fail-closed account/subscription UX and unrelated node/Hybrid continuity.
- [ ] Exact-head Backend/Agent/Workspace/installer/website/traffic CI all green.
- [ ] Independent review of exact final diff and a deliberate scoped merge approval.
- [ ] **Only after** code, tests and dependency review: close obsolete PRs and delete obsolete branch refs; never rewrite `main` or claim GitHub PR metadata/history can be erased.

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
