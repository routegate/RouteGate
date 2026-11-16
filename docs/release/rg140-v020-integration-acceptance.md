# RG-140 / RouteGate v0.2.0 — Integration preview acceptance ledger

**State:** Draft, NON-DEPLOYABLE integration preview. This is not an approval to merge into `main`, tag/publish `v0.2.0`, execute a release workflow, or change US/FI/NL production.

## Exact origins and deterministic assembly

- **Security/runtime baseline:** RG-140 stacked Draft PR #783, commit `61ecfbb889f7b78b143e08387ac94318e99b5fe8`, based on current `main` at `62a08c922e105e4a1af19cdf9fa8a582104133a0`.
- **Installer/release-preview baseline:** Draft PR #721, commit `4a6f6371bd908e821acd65ec8aac414d4768920d`, same `main` ancestor; **different, diverged history**.
- Compared both heads independently with `main`. The GitHub compare for the large RG-140 branch reports only the first 200 files; no assumption was made that unseen paths were unchanged.
- Verified **every one** of the 29 non-overlapping release paths in `main`, #783 and #721 by blob hash. On #783, each path was still identical to `main` or absent. Exactly those 29 #721 blobs were overlaid as ONE commit on the #783 history. No RG-140 existing modified path was replaced.
- Identified 34 paths shown as changed in both branch comparisons: 23 blob-identical, 11 divergent. In the first assembly the **11 divergent paths remain at RG-140 versions**, except `.github/workflows/ci.yml`, which is explicitly combined to retain all RG-140 race/Agent/Manager checks AND add native Hybrid, Grafana, public Quick Start, bundle manifest and tagless-release rehearsal gates.

## Divergent paths requiring deliberate review

- `.github/workflows/ci.yml` — now explicitly unioned, with no RG-140 check intentionally removed.
- `agent/internal/tasks/credential_removal.go` — the later RG-140 shared runtime mutation lock and prior-result recovery evidence must win over any older standalone receipt lock; review the resulting live behavior and corresponding tests.
- `agent/internal/tasks/credential_removal_systemd_test.go` — RG-140 adds extra historical replay / crash witnesses, retain them.
- `backend/internal/db/migration_ordinal_preflight_test.go` — RG-140 tests migrations 162–167 and PL/pgSQL quoting; do not roll back to the #721 schema 161 expectation.
- `backend/internal/buildinfo/buildinfo.go`, `backend/internal/buildinfo/buildinfo_test.go` — current expected schema 167, not 161.
- `backend/internal/db/client_profile_post_restore_repair_integration_test.go`, `client_profile_uniqueness_drift_integration_test.go`, `credential_revocation_schema_upgrade_integration_test.go`, `migrator_integration_test.go` and `vpn_subscription_token_pkey_repair_integration_test.go` — tests must expect latest migration 167, not stale 161.

The shared `backend/migrations/000161_credential_revocation_preparations.{up,down}.sql` and `backend/internal/db/migrator.go` were verified **blob-identical** on both source branches. The merged preview retains the RG-140 migrations 161–167.

## Acceptance gates (all required, not yet signed off)

- [ ] All combined CI jobs SUCCESS on the EXACT integration preview HEAD, including RG-140 Agent/Backend race and PostgreSQL crash/transport-loss tests, frontend, website, installer, Manager→Reality and Workspace Integration.
- [ ] Clean disposable Ubuntu 24.04 Hybrid install using the candidate installer + pinned bundle with correct release manifest and checksums; verify Manager/PostgreSQL/Agent, HTTPS, dark-only frontend and service readiness.
- [ ] Clean disposable Ubuntu 24.04 Hybrid WITH Grafana offered at install time, with Prometheus and safe retry on sidecar failure. No silent retroactive installation or production server mutation.
- [ ] Confirm website and README Quick Start point only at a verified published stable version (not an unreleased v0.2.0 snapshot). Reject v0.1.0/current-main installer + bundle incompatibilities non-mutatingly.
- [ ] Test scoped VLESS/Reality removal without unrelated account/port/protocol drift, full Agent restart, crash/replay recovery, existing account subscription continuity and Last-Confirmed-Applied fallback; a subscription link revoked or account suspended does NOT automatically prove old VPN traffic stopped.
- [ ] Verify no unexpected runtime mutation or recovery unlock from any offline `prepared_unattested`, `reply_matched_unattested` or manual history state; no protected enrollment auto-activation for legacy nodes.
- [ ] Review launch/migration order from a snapshot of existing schema 160 / 161 to 167, forward-only upgrades, fail-closed downgrade/rollback, and resulting application read-only failure behavior.
- [ ] Independent isolated external VPS canary smoke test (not US/FI/NL), including VLESS Reality + Hysteria2, client subscription refresh, network loss, restart and installer recovery. Production remains read-only until separately approved.
- [ ] Review open [installer parity blocker #715](https://github.com/routegate/RouteGate/issues/715), revocation [#545](https://github.com/routegate/RouteGate/issues/545) and subscription continuity [#538](https://github.com/routegate/RouteGate/issues/538). Close only with evidence.

## Strict exclusions

Do NOT merge this preview to `main` on the basis of Draft CI alone. Do NOT tag/publish a new stable release, alter the live website Quick Start, update public docs to an unpublished tag, run any production installer, restart US/FI/NL Agents or change existing working VPN credentials. A release candidate requires explicit operator authorization and separate independent sign-off.

Historical Agent enrollment HMAC proves bearer possession, **not physical host attestation**. PostgreSQL historical evidence is not resistant to privileged DB/VM rollback. Keep these limits truthful and documented.
