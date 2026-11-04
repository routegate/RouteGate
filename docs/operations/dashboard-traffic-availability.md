# Dashboard traffic widget: availability and recovery

## Why the card shows no traffic

The 30-day Dashboard chart reads
`GET /api/v1/dashboard/traffic` and **never** estimates or reconstructs
historical bytes. The Manager computes `dailyAvailable` from actual rows in
`traffic_usage_daily` during the last 30 UTC calendar days. It also emits a
30-day array filled with zero values **even when no source reports exist**.
For this reason, the daily row count or aggregate zero is not proof that
accounting works.

The UI now distinguishes these cases:

| Query / payload | Widget | Next action |
| --- | --- | --- |
| Request pending | Loading statistics | Wait for request |
| HTTP failure, missing/invalid daily payload after success | Could not load traffic statistics | Retry request; inspect Manager API and database |
| HTTP 200, `dailyAvailable=false` | No measurements in last 30 days | Check VPN nodes and traffic collection |
| HTTP 200, `dailyAvailable=true`, valid rows all zero | 0 B and real chart rows | This is a known zero, not missing data |
| HTTP 200, `dailyAvailable=true`, valid nonzero rows | Real total/chart | No repair needed |

The placeholder animation has been removed. It was decorative, not actual
traffic. This work changes presentation and status semantics only; it does
**not** enable traffic capture on a production VPN host.

## Read-only diagnosis of a deployed Manager

1. In the authenticated Admin UI, open the Dashboard, browser developer
   tools / Network, and inspect the **status code** and response **fields**
   for `/api/v1/dashboard/traffic`. Do not copy or publish authorization
   headers or tokens.
2. If the request fails (401/403/404/500/network), investigate the
   authentication, deployed Manager version, logs, database schema and API
   availability as appropriate. Do not treat the failure as zero traffic.
3. If HTTP 200 returns `dailyAvailable=false`, inspect whether the VPN
   Agent has traffic collection enabled and whether real usage reports
   are reaching the Manager. An online Agent does not imply enabled
   accounting.
4. Where authorized and safe, check the database **read-only** for the
   schema version and recent counts in `traffic_usage_events` and
   `traffic_usage_daily`, and inspect the installed sing-box build tags
   and active loopback stats configuration without printing credentials.
5. If available/true is returned but rows are invalid, treat this as
   API/data corruption and fix the backend; do not replace with mock data.

## Deployment prerequisite (not done by this UI fix)

Native sing-box per-account accounting was merged in PR #669. The documented
2026-10-31 live checkpoint showed the US Manager still at schema 159; the
new durable stream feature uses migration 160. It had **not** been
installed or applied to the live fleet at that checkpoint. Check more
recent operational evidence before assuming this state is unchanged.

The scoped traffic rollout script intentionally disables install/apply:
its metadata-pinning fallback cannot preserve a consistent Manager
version/commit and bootstrap artifact identity. **Do not bypass this guard
or deploy a mismatched binary.** Follow the release/bootstrap verified
publication and recovery procedure after it has been corrected and tested.
Coordinate with the open RG-140 deployment/recovery work before changing
US. Confirm Manager/Agent compatibility and exact release artifacts, then
run staged rollout under the existing lock and recovery controls.

After Manager/schema installation and Agent capability readiness, use
the normal render/validate/apply workflow **only if** its verified runtime
diff is limited to the intended loopback statistics API. Applying a live
VPN configuration can briefly interrupt existing clients; no live apply
is authorized by this UI PR. Validate actual VLESS client traffic, Agent
reports, daily rollup growth and the API's `dailyAvailable=true` before
claiming the chart is operational. Do not manufacture old usage.

Hysteria2 and native WireGuard are separate accounting sources and are not
automatically covered by the sing-box VLESS collector.

References:

- `docs/features/singbox-traffic-accounting.md`
- `docs/operations/traffic-accounting-checkpoint-20261031.md`
- `docs/operations/traffic-accounting-rollout.md`

## Regression verification

From `frontend/`, in a complete repository checkout:

```sh
npm ci
npm run i18n:check
npm run test:dashboard-traffic
npm run test:ui
node --test scripts/dashboard-motion.test.mjs
node --test scripts/dashboard-motion.browser.test.mjs
npm run build
```

Browser and end-to-end checks remain required for the deployed view at
390px and 1440px. Verify the empty state is legible, contains no fake bars,
and displays only the appropriate retry/navigation action.
