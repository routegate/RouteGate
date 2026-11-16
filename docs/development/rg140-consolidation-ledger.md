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
