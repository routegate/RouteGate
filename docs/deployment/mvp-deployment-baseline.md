# MVP Deployment Baseline

## Status — historical record only

**RouteGate v0.1.0 MVP — original public deployment baseline, NOT the current
v0.2.0 installation guide.** The supported current-feature first-install
entrypoint, once a verified release is published, is
[routegate.org — Quick Start](https://routegate.org/#install).
Do not use this older baseline to initiate a new v0.2.0 installation.
See [release readiness #715](https://github.com/routegate/RouteGate/issues/715).

This document defines the canonical deployment profile for the first public RouteGate release. Docker Compose remains available for local development, but it is not the supported public installation path.

## Supported v0.1.0 profile

The live-validated MVP profile is:

- Ubuntu 24.04 LTS;
- amd64;
- one self-hosted VPS;
- native systemd services;
- local PostgreSQL;
- RouteGate Manager + Admin UI;
- nginx reverse proxy and static frontend delivery;
- Let's Encrypt HTTPS;
- local RouteGate Agent registered automatically by the installer;
- sing-box installed later through RouteGate;
- VLESS / Reality as the validated VPN path.

Canonical architecture:

```text
Internet
  │
  ├─ TCP 443  → nginx / RouteGate HTTPS
  │               │
  │               ├─ Admin UI
  │               └─ Manager API → PostgreSQL (loopback/local only)
  │
  └─ TCP 8443 → sing-box / VLESS / Reality

RouteGate Manager → local RouteGate Agent → sing-box
```

## Prerequisites

Before installation:

1. Provision a clean Ubuntu 24.04 LTS amd64 VPS.
2. Create a DNS `A` record for the desired RouteGate FQDN pointing to the VPS public IPv4 address.
3. Ensure inbound TCP 80 and 443 are reachable for HTTP/TLS setup.
4. Keep SSH access working and use `root` or an account with functional `sudo`.
5. Avoid reusing a host that already contains unrelated active PostgreSQL/web services unless you understand and resolve the detected conflicts first.

The installer preserves SSH authentication policy and stops rather than silently overwriting unrelated host services or unowned RouteGate files.

## Install — historical scope

The original `v0.1.0` deployment used an immutable tag and a multi-step
installer. That release remains archived in
[release notes](../release/v0.1.0.md) for maintenance and historical
reproduction; it does not contain the verified one-copy/current-feature
wizard and should not be recommended for new installations.

For an ordinary **new** VPS, use the
[First-install guide](../guides/first-install.md) and verified website
[Quick Start](https://routegate.org/#install) **only after** the current
stable release and complete artifacts are published. Never mix `v0.1.0`
bundles with newer installer scripts.

## What a successful platform installation provides

A completed installer run owns and verifies:

- PostgreSQL installation and a RouteGate-specific local database/role;
- RouteGate Manager binary and migrations;
- production Admin UI assets;
- RouteGate Agent binary and systemd service;
- nginx configuration;
- Let's Encrypt HTTPS;
- local All-in-One Server creation;
- automatic one-time Agent registration and persistent Agent identity;
- single-use administrator `/setup` activation link;
- resumable installer state and a root-only installer log.

The installer does **not** install or start sing-box automatically.

## First access

After installation, the installer prints a URL in this form:

```text
https://vpn.example.com/setup#token=<single-use-token>
```

Open it before expiration and choose the administrator password. After successful activation, RouteGate signs the administrator in automatically.

The root-only recovery file is:

```text
/root/routegate-first-login.txt
```

Remove it after activation and password verification:

```bash
sudo rm -f /root/routegate-first-login.txt
```

## Guided VPN bring-up

A fresh installation is intentionally not considered VPN-ready merely because the management platform is running.

RouteGate guides the remaining lifecycle from actual system state:

```text
Platform ready
→ local Server / Agent connected
→ Install sing-box
→ configure recommended VLESS / Reality
→ create first VPN account
→ Deploy VPN
   render → validate → apply → start/restart → health check
→ open persistent client profile
→ QR / VLESS link
→ client connected
```

Installing sing-box and running sing-box with a valid RouteGate-generated configuration are separate states. The Config Deploy apply lifecycle owns the first real VPN Core startup after a valid configuration exists.

## Validated client path

Final clean-host MVP acceptance validated real VPN connectivity using:

- V2Box;
- V2RayTun.

Persistent client profiles were validated across reload/re-login behavior, including a working Reality client fingerprint of `firefox`.

RG-101C advanced compatibility auto-tuning remains Post-MVP / Planned and is not required for v0.1.0.

## Service verification

On the installed host:

```bash
sudo systemctl status postgresql nginx routegate-manager routegate-agent
curl -fsS https://vpn.example.com/api/admin/health
```

After the VPN has been deployed through RouteGate:

```bash
sudo systemctl status sing-box
```

The exact VPN runtime state should also be inspected through RouteGate because installed, stopped, running, degraded, and failed states are represented explicitly by the Manager/Agent workflow.

## Installer state and logs

```text
/etc/routegate/install-state.env
/var/log/routegate-installer.log
/var/lib/routegate-installer/
```

A completed RouteGate-owned installation can be rechecked by rerunning the installer. The completed path performs health/idempotency checks instead of reinstalling or rotating credentials.

## Release artifacts

The v0.1.0 GitHub Release publishes:

```text
routegate-v0.1.0-linux-amd64.tar.gz
routegate-v0.1.0-linux-arm64.tar.gz
SHA256SUMS
```

The release workflow builds and verifies both bundle architectures. The **supported live Clean VPS installation contract for v0.1.0 is amd64**, because that is the architecture accepted on the production-like clean-host E2E path.

## Development profile

For contributors and local development only:

```bash
git clone https://github.com/routegate/RouteGate.git
cd RouteGate
make dev
```

This Docker Compose/Vite environment is useful for development and automated checks. It must not be confused with the supported v0.1.0 public deployment contract above.

Run the full repository validation with:

```bash
make check
```

## Explicit MVP deployment non-goals

v0.1.0 does not claim support for:

- Kubernetes or HA deployment;
- an appliance image or appliance OS;
- managed/automatic RouteGate upgrades;
- external PostgreSQL as the canonical installer topology;
- additional VPN Cores;
- additional VPN protocols beyond the validated VLESS / Reality path;
- official RouteGate mobile clients;
- automatic client compatibility tuning.

## Acceptance result

The final clean-host lifecycle passed on the production-like `us.routegate.org` validation host:

```text
Clean VPS
→ installer
→ secure /setup
→ local Agent registration
→ Guided Workflow
→ sing-box installation
→ VLESS / Reality
→ VPN account
→ Config Deploy
→ persistent client profile
→ real VPN connectivity
→ reboot
→ automatic service recovery
→ real VPN connectivity after reboot
```

This is the RouteGate v0.1.0 MVP deployment baseline.
