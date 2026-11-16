# RouteGate documentation

This directory contains **current development documentation, release
documentation, implementation notes and historical records**. These categories
have different acceptance boundaries. A feature described on `main` is
**not automatically part of the latest published stable release**.

## Start here

- [First install on a clean VPS](guides/first-install.md) — ordinary users
  should take one verified command from [routegate.org — Quick Start](https://routegate.org/#install)
  **only when** a current supported release is published. If the website still
  presents an obsolete `v0.1.0` command, do not use it for current features.
- [Clean VPS installer reference](deployment/clean-vps-installer.md) —
  installation preflight, ownership, DNS/HTTPS and recovery boundaries.
- [PostgreSQL migration safety](development/database-migrations.md) —
  schema-history compatibility, forward-only ordinals and RG-140 collisions.
- [Add a remote VPN node](guides/add-vpn-node.md) — Manager/Agent onboarding
  on versions that include it.
- [Recovery toolkit](operations/recovery-tool.md) and
  [VPS baseline](operations/vps-baseline.md) — operational support.
- [Actual published GitHub Releases](https://github.com/routegate/RouteGate/releases) —
  the source of truth for which tags and assets are genuinely downloadable.

## How to interpret documentation

| Area | Meaning |
| --- | --- |
| [Architecture](architecture/) and [Decisions](decisions/) | Design and implementation contracts; may describe changes on `main` not yet publicly released. |
| [Features](features/), [API](api/) and [Security](security/) | Behavior and technical contracts in the source tree; actual availability depends on published version and testing. |
| [Guides](guides/) and [Deployment](deployment/) | Current user-facing instructions; **do not** replace a missing verified installer with an older or mutable one. |
| [Operations](operations/) and [Observability](observability/) | Runbooks, reviews, historical checkpoints and managed-service references. Check the target release before applying any command. |
| [Release notes](release/) | Version-specific records. Historical release commands are preserved as history, not current first-install recommendations. |

**Explicit historical documents:** the
[v0.1.0 release notes](release/v0.1.0.md),
[original MVP baseline](deployment/mvp-deployment-baseline.md), and
[v0.1.0 release-readiness checklist](deployment/release-readiness-checklist.md).
They do not authorize release of `v0.2.0`.

**Pending release:** the new verified one-command installer, public
Let's Encrypt/VPS acceptance and publication are tracked in
[issue #715](https://github.com/routegate/RouteGate/issues/715).
Successful CI tests with temporary test TLS are not proof of real external
first-install acceptance. Release-dependent security and subscription
continuity issues must also be reviewed.

A note in [Manager artifact staging](architecture/manager-artifact-staging.md)
contains the historical `ikaevus/RouteGate` release trust namespace. This is
a **security-sensitive implementation boundary**, not a public download link.
Do not change the trust/signer identity merely to fix documentation branding;
that migration requires a separate reviewed code-and-provenance change.

## Keeping this documentation accurate

When modifying release assets, installers, API capabilities or website
Quick Start, update the matching docs in the same PR. Run:

```sh
node scripts/check-documentation.mjs
```

This deterministic check covers tracked Markdown/TXT paths and prevents
obsolete first-install snippets from re-entering the primary guides.
The companion `scripts/check-release-doc-assets.mjs` verifies exact pinned
GitHub tag/release-asset URLs through the GitHub API. The
[Documentation consistency workflow](../.github/workflows/documentation.yml)
runs these checks on pull requests, on `main`, and in a scheduled link-rot
audit. Other third-party HTTP URLs require a separate online review;
an offline success does not certify them reachable.
