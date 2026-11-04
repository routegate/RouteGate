# US traffic accounting — safe diagnosis and rollout prerequisites

## Current status

The manual **Traffic accounting scoped US rollout** workflow currently
supports **only** `mode=inspect`. The fixed Ops Bridge issue #268 also accepts
exactly `operation=traffic-inspect` to run the same read-only helper from
trusted main, with the same host-side lock and without accepting caller paths.
Both entry points use authenticated, SSH host-key-verified read-only diagnostics
under the existing production-like environment and deployment lock. **Install and apply are intentionally disabled** in both
GitHub Actions inputs and the Python runner. Do not circumvent either guard.

The updated inspection does not depend on scraping Go linker `-ldflags` from
a trimmed Manager binary. That technique cannot establish trustworthy
bootstrap identity. Inspection also does not fail merely because the
legacy runtime lacks a statistics API or the Agent has accounting disabled:
those are exactly the findings it is supposed to report.

## Inspect-only output

The helper verifies the registered US Agent/server binding and reads:

- Database schema: whether migration
  `000160_traffic_report_checkpoints` is present.
- Active transfer and config-apply job **counts** (safety blockers).
- Installed sing-box `with_v2ray_api` build tag (true, false or unknown).
- Existing Agent `traffic_collection_enabled` setting.
- Active sing-box statistics API enabled and configured on
  `127.0.0.1:18085` (true, false or unknown).
- Number of native sing-box usage reports **for this US node** received
  in the last 30 days.
- Number of `traffic_usage_daily` rows in the **fleet-wide** current
  30-day UTC Dashboard window.

Database queries run with PostgreSQL read-only defaults. The workflow
reports no account IDs, bearer secrets, rendered configs, runtime JSON,
credentials or usage-event contents. The deployed Dashboard API is
**not** contacted by this diagnostic, and `dashboard_daily_available_from_database`
is a database-only indication, not a verified production API result.

A read-only run must never start a Manager, Agent, sing-box, WireGuard,
Hysteria2 or other VPN service; must not change settings or apply a
configuration; and must not publish/bootstrap artifacts.

## Interpreting findings

| Evidence | Meaning | Next step |
| --- | --- | --- |
| Schema 159 | Existing Manager lacks migration 160 | Review the verified release and migration plan; no direct install |
| Schema 160, Agent collection false | Counter reporting not enabled | Check Agent capability and staged enabling procedure |
| Runtime missing `with_v2ray_api` | sing-box build cannot expose supported native counters | Replace only through tested release/update process |
| Statistics API not configured | Active VPN config has no native counters | Prepare and review **stats-only** render diff; do not apply automatically |
| Native report count zero | No evidence of native collection from US in 30 days | Investigate Agent, real VLESS usage, transport and DB ingestion |
| Daily rows zero | Dashboard has no usage evidence in 30-day UTC window | Preserve N/A/no measurements until data actually arrives |
| Daily rows positive | DB has some historical measurements | Verify authenticated Dashboard API, timestamps and actual chart |
| Probe unknown | Cannot safely determine status | Check host path/service manually, without guessing |

One or more findings may coexist. A healthy VPN connection does not imply
that the accounting pipeline is enabled. Older traffic cannot be
reconstructed from newly enabled counters.

## Blocker for live installation and apply

The 2026-10-31 checkpoint found US at schema 159 and recorded no native
accounting deployment. This is **historical evidence**, not a verified
current live state. The old scoped rollout attempted to infer bootstrap
metadata from Manager binary strings, while changing Manager version/commit
without publishing matching bootstrap artifacts. This can break future
node onboarding and is not acceptable.

To proceed beyond inspection, use the existing tested release and
bootstrap-publication path, based on one pinned commit, matching bundle
version, Manager GitCommit, installer digest and bootstrap base URL.
Verify the release manifest and artifact provenance before installation.
Never spoof an old build identity or override a safety refusal.

The later stages require **separate, reviewed authorization** because
updating Manager/Agent may affect control-plane availability and
applying a VPN config can briefly interrupt existing VPN sessions:

1. Recheck live read-only evidence and active jobs, RG-140 overlap and
   US account/access continuity. Coordinate changes with RG-140 #545.
2. Prepare a matching full release and published bootstrap artifacts
   with checksum and provenance verification; record exact identifiers.
3. Stage recovery, backup and rollback checks before any control-plane
   install. Preserve credentials, device links, routes and existing data.
4. Upgrade Manager/migration and compatible Agent in a controlled window.
   Do not restart VPN runtime during the initial control-plane stage.
5. Render and validate a new config; compare with the **current applied
   and active configs**, rejecting any unrelated change. Only
   `v2ray_api` statistics config is in scope.
6. Obtain explicit approval for any VPN config apply. Afterward generate
   real VLESS traffic, verify native Agent reports, deduplicated events,
   daily rollup growth and `GET /api/v1/dashboard/traffic`
   `dailyAvailable: true`.

Native Hysteria2 and WireGuard accounting are separate follow-up work.
No fake reports, mock usage, or historical backfill are permitted.

## Validation

From a full repository checkout:

```sh
python3 -m unittest scripts/test_traffic_accounting_rollout.py
```

The repository's Frontend CI/installer tests also run this suite. An
additional test PostgreSQL integration runs only when
`ROUTEGATE_TEST_DATABASE_URL` is set. The manual diagnostic is not
automatically invoked by CI and does not run on production without a
separate authorized workflow dispatch.

See also:
- `docs/features/singbox-traffic-accounting.md`
- `docs/operations/traffic-accounting-checkpoint-20261031.md`
- `docs/operations/publish-node-bootstrap.md`
- `docs/operations/dashboard-traffic-availability.md` (from the separate
  Dashboard UI PR, once merged)
