<p align="center">
  <a href="https://routegate.org">
    <img src="brand/logos/svg/logo-primary-horizontal-dark.svg" alt="RouteGate" width="620">
  </a>
</p>

<p align="center">
  <strong>Open-source, self-hosted Linux VPN Management Platform.</strong>
</p>

<p align="center">
  Operate VPN nodes, accounts, protocols, routing, verified platform updates, secure client configuration delivery, and operational state from one control plane.
</p>

<p align="center">
  <a href="https://routegate.org">Website</a> ·
  <a href="docs/">Documentation</a> ·
  <a href="https://routegate.org/#roadmap">Roadmap</a> ·
  <a href="SECURITY.md">Security</a> ·
  <a href="CONTRIBUTING.md">Contributing</a>
</p>

<p align="center">
  <img alt="License: AGPLv3-or-later" src="https://img.shields.io/badge/license-AGPLv3--or--later-2563EB">
  <img alt="Backend: Go" src="https://img.shields.io/badge/backend-Go-07111F">
  <img alt="Frontend: React and TypeScript" src="https://img.shields.io/badge/frontend-React%20%2B%20TypeScript-07111F">
  <img alt="Database: PostgreSQL" src="https://img.shields.io/badge/database-PostgreSQL-07111F">
</p>

---

## What is RouteGate?

RouteGate is an independent open-source control plane for operating self-hosted Linux VPN infrastructure.

```text
RouteGate Manager → RouteGate Agent → managed VPN runtime
```

The boundary is intentionally simple:

- **Manager** owns desired state, the Admin UI and API, PostgreSQL-backed data, account and routing policy, configuration lifecycle, software-update orchestration, audit history, secure client delivery, and aggregated operational state.
- **Agent** runs on managed Linux nodes and performs authenticated, allow-listed host operations such as installation, validation, apply, service control, diagnostics, health checks, rollback, and verified RouteGate platform updates.
- **VPN runtimes** remain replaceable behind explicit protocol/core adapters. RouteGate manages only combinations for which it can own the complete lifecycle safely.

Remote Agents do **not** connect directly to PostgreSQL, and RouteGate does not expose an arbitrary remote-shell channel from Manager to nodes.

RouteGate is designed as an infrastructure product: administrators work with nodes, VPN accounts, protocols, routing policies, deployment state, client compatibility, update state, and next actions instead of assembling those workflows from scripts and unrelated service-specific panels.

## Project state

**RouteGate v0.1.0 is the first public MVP release. `main` has moved significantly beyond that release baseline.**

The canonical clean-host path remains the compatibility and production-like validation baseline:

```text
Clean Ubuntu 24.04 LTS host
→ one-command RouteGate installation
→ PostgreSQL + Manager + Admin UI + nginx/HTTPS + local Agent
→ single-use /setup activation
→ Guided Workflow / Next Action First
→ managed VPN runtime installation
→ VLESS / Reality configuration
→ VPN account creation
→ render → validate → apply → health check → rollback boundary
→ persistent client access / QR / subscription
→ working VPN connection
→ reboot
→ service recovery
→ working VPN connection after reboot
```

The production-like validation environment is operated at `us.routegate.org` using native systemd services, PostgreSQL, nginx, and HTTPS.

Development on `main` now extends the original MVP in several major areas:

- multi-node deployment roles, remote VPN Node onboarding, multiple managed protocol families, multi-runtime apply, node groups, and explainable Automatic Selection;
- verified RouteGate release artifacts, explicit one-click platform updates, and durable one-node-at-a-time VPN-node rollout orchestration;
- opaque, revocable client subscription URLs with capability-aware delivery and client-specific routing adapters;
- named online VPN-user visibility across managed protocols and expanded operational Admin UI workflows.

> **Release note:** features described as available on `main` may not exist in the published v0.1.0 release bundle. The v0.1.0 release notes remain the source of truth for that specific release.

### Validation and implementation status

| Area | Current status |
|---|---|
| Clean Ubuntu 24.04 LTS All-in-One / Hybrid deployment | Production-like validated baseline |
| VLESS / Reality | Production-like validated public VPN path |
| Remote VPN Node bootstrap and inventory | Implemented on `main` |
| Native WireGuard | Managed adapter implemented on `main` |
| Hysteria2 | Managed adapter implemented on `main`; dedicated and Hybrid-node ACME lifecycles supported |
| Shadowsocks 2022 | Managed adapter implemented on `main` |
| MTProto / FakeTLS | Managed adapter implemented on `main` |
| Multiple protocols on one VPN node | Manager/Agent multi-runtime lifecycle implemented on `main` |
| Node groups and explainable Automatic Selection | Preview/apply workflow implemented on `main`; unattended failover is intentionally not enabled |
| Verified RouteGate platform updates | Release manifest, provenance verification, recoverable host update engine, and explicit Admin workflow implemented on `main` |
| Multi-node RouteGate update rollout | Durable ordered, proof-gated, one-node-at-a-time rollout with explicit Admin controls implemented on `main`; unattended updates are disabled |
| Opaque client subscription delivery | Implemented on `main` with hash-only token storage, rotation/revocation, no-store handling, and protected `/sub/` delivery |
| Client capability-aware delivery | Implemented for Hiddify (recommended), sing-box, v2rayN, v2rayNG, and Generic (best-effort) fallback for other clients |
| Named online VPN users | Implemented across managed protocol presence collectors and grouped by account/server/node in the Admin UI |
| Per-device access (Access & Devices) | One VPN account may have multiple devices, each with its own opaque subscription link and independent revoke/rotate lifecycle |

Implementation support and production-like validation are deliberately treated as different claims. A detected binary, upstream feature, or client capability is not considered a RouteGate-managed capability until the relevant settings, credentials, render, validation, apply, rollback, health, delivery, redaction, and compatibility boundaries are controlled and tested by RouteGate.

## Deployment model

RouteGate uses explicit node roles rather than a permanent root-server hierarchy.

| Role | Intended components | Agent required | VPN configuration deploy |
|---|---|---|---|
| **Management Node** | Manager, PostgreSQL, Admin UI, nginx/HTTPS | No | No |
| **VPN Node** | Agent and managed VPN runtime(s) | Yes | Yes |
| **Hybrid Node** | Management plane + VPN plane | Yes for VPN plane | Yes |

The familiar single-server installation is a **Hybrid Node**. Additional VPN Nodes register with Manager using a short-lived, single-use bootstrap token and then report heartbeat, capabilities, versions, runtime state, telemetry, presence, and task results through the Manager API.

Manager evaluates three things separately before offering an action:

1. **role** — what the node is intended to host;
2. **capability** — what the registered Agent can manage;
3. **runtime state** — what is installed and healthy now.

See [Platform Expansion Architecture](docs/architecture/platform-expansion.md) and the [Remote VPN Node guide](docs/deployment/remote-vpn-node.md).

## Managed protocol model

RouteGate separates **VPN Core**, **Protocol**, **Transport**, and **Security** instead of treating every VPN feature as one interchangeable mode.

| Protocol | Managed runtime | Transport / security model | Status on `main` |
|---|---|---|---|
| **VLESS / Reality** | sing-box | TCP + Reality / XTLS Vision | Managed and production-like validated |
| **WireGuard** | native `wireguard-tools` / `wg-quick` | UDP + WireGuard cryptography | Managed |
| **Hysteria2** | Hysteria | QUIC + TLS / ACME | Managed; direct ACME on VPN Nodes and loopback nginx ACME bridge on Hybrid Nodes |
| **Shadowsocks 2022** | sing-box | TCP + AEAD-2022 | Managed |
| **MTProto / FakeTLS** | mtg | TCP + FakeTLS | Managed |

A VPN client profile may use `Auto` to inherit the node default or explicitly select one managed protocol. Manager resolves the effective protocol for every active account before rendering configuration, so one VPN Node can require multiple managed runtimes at the same time.

VLESS and Shadowsocks are composed into the shared sing-box runtime. Native WireGuard, Hysteria2, and MTProto retain separate runtime/service ownership. Agent validates all required runtimes before promotion and rolls already-applied runtimes back if a later step in the same node deployment fails.

Hysteria2 owns its VPN-plane certificate locally through ACME. RouteGate does not reuse or distribute the Manager nginx private key. On Hybrid Nodes, nginx forwards only the HTTP-01 challenge for a separate Hysteria DNS hostname to a fixed loopback listener; TCP 443 remains owned by RouteGate while Hysteria uses UDP 443.

Cross-node atomic deployment remains a separate problem and is not implied by multi-runtime support on one node.

## Secure client configuration delivery

RouteGate separates stable user-facing subscription identity from protocol-specific connection material.

The primary client credential is an opaque HTTPS URL:

```text
https://vpn.example.com/sub/<opaque-token>
```

The stable URL does not encode account IDs, node IDs, protocol names, addresses, ports, UUIDs, Reality parameters, or other infrastructure metadata. Subscription tokens are cryptographically random bearer credentials; RouteGate stores only their hashes and supports rotation, revocation, optional expiry, and usage tracking.

The `/sub/` boundary can return a representation appropriate for the selected client and effective protocol. Raw protocol material remains available for interoperability and diagnostics, but it is not the preferred stable delivery surface.

Routing Profiles remain the **single routing-policy source of truth**. The client compatibility layer only chooses or serializes a representation capable of carrying the already-resolved policy; it does not create a second routing engine.

### Current client compatibility model

| Client | Connection delivery | RouteGate routing delivery | Current Smart Routing claim |
|---|---|---|---|
| **Hiddify** | sing-box JSON on supported VLESS path | Routing Profile embedded in the same config | Full on validated VLESS path |
| **sing-box** | sing-box JSON on supported VLESS path | Routing Profile embedded in the same config | Full on validated VLESS path |
| **v2rayN** | Standard Base64 share-link subscription | Separate native custom-rules URL through the same opaque token (adapter implemented, automated tests; manual real-client validation remains pending, historically tracked in #392) | Supported with client-side setup |
| **v2rayNG** | Standard Base64 share-link subscription | Not offered — native routing import is not independently validated on this client | Connection only (not inherited from v2rayN) |
| **Generic** (any other client) | Conservative RG-115 auto delivery | Not assumed | Connection only |

RouteGate follows a **no silent downgrade** rule: successful protocol connectivity never implies that a client can faithfully reproduce the assigned Routing Profile. The Admin UI surfaces the selected client's compatibility state and remaining limitations.

See [Secure Config Delivery](docs/architecture/secure-config-delivery.md), [Client Capability-Aware Delivery](docs/architecture/client-capability-aware-delivery.md), and the [Client Compatibility Matrix](docs/architecture/client-compatibility-matrix.md).

## Verified platform updates

RouteGate's update path is built around explicit trust and recoverability rather than downloading and replacing binaries blindly.

Release outputs include a machine-readable `release-manifest.json` that binds the release version to the exact Git commit, expected database migration, supported platform artifacts, sizes, and SHA-256 digests. Official release workflows additionally produce GitHub Artifact Attestations / Sigstore provenance for the manifest and platform bundles and verify them against the fixed RouteGate repository and release-workflow identity.

The shared host-update engine performs bounded, role-aware operations against known RouteGate paths:

```text
release discovery
→ manifest / provenance verification
→ platform preflight
→ backup
→ stage
→ apply
→ database migration validation
→ Manager / Agent health proof
→ success or rollback / recovery
```

The Admin UI exposes an explicit administrator-approved update workflow. Remote VPN Nodes use durable ordered rollout state with proof-gated advancement and one-node-at-a-time controls. Automatic scheduling, silent forced updates, broad fleet concurrency, and unattended retry remain disabled.

VPN runtimes are deliberately outside the RouteGate platform-update transaction wherever possible: updating Manager, Agent, UI, migrations, or managed platform files should preserve an already-running VPN data plane rather than unnecessarily replacing VPN runtime state.

See [Updates, Releases, and Versioning](docs/architecture/versioning-and-updates.md), [Verified Host Updates](docs/architecture/verified-host-updates.md), and [Multi-Node Update Rollout](docs/architecture/multi-node-update-rollout.md).

## Install RouteGate

The current public clean-host installation contract is **Ubuntu 24.04 LTS on amd64** with native systemd services, local PostgreSQL, nginx, and Let's Encrypt TLS. Remote VPN Node onboarding supports amd64 and arm64.

Before installation:

- create a DNS `A` record for the RouteGate hostname pointing to the host;
- make TCP ports 80 and 443 reachable;
- connect as `root` or a user with working `sudo`.

Select a **published stable tag** from
[GitHub Releases](https://github.com/routegate/RouteGate/releases/latest) or
use the live, version-pinned quick-start on [routegate.org](https://routegate.org/).
The installer and bundle **must come from the same immutable tag**; do not mix
`main/install.sh` with a historical release. The old `v0.1.0` is an archived
MVP and does not include newer UI/features (see [#715](https://github.com/routegate/RouteGate/issues/715)).

When installing from the README, replace `REPLACE_WITH_STABLE_TAG` below with
the exact published tag you reviewed. Download the installer locally, inspect
it, then run it with privileges:

```bash
VERSION=REPLACE_WITH_STABLE_TAG
curl -fL --proto '=https' --tlsv1.2 \
  "https://raw.githubusercontent.com/routegate/RouteGate/${VERSION}/install.sh" \
  -o routegate-install.sh
less routegate-install.sh
sudo bash routegate-install.sh --version "${VERSION}"
rm -f routegate-install.sh
```

Published releases built with the current release workflow also attach
`install.sh`, `install-agent.sh`, `install-grafana.sh`,
`INSTALLER_SHA256SUMS`, and an offline GitHub Artifact Attestation bundle.
That release-asset path is the preferred high-assurance bootstrap for new
releases.

Before any APT network operation, the installer also checks the host's active package sources and fails closed unless they are inside RouteGate's approved repository boundary. Approved provider mirrors are explicit; unrelated third-party sources are not silently trusted or rewritten.

The installer downloads the selected published RouteGate release bundle and verifies it against `SHA256SUMS`. Always match the pinned installer tag to its archive. For older v0.1.0 installations and recovery limitations, see the [Clean VPS Installer guide](docs/deployment/clean-vps-installer.md).

After installation, open the single-use `/setup` link printed by the installer, choose the administrator password, and continue through the guided Dashboard workflow.

For a task-oriented walkthrough, see [Install RouteGate on a new VPS](docs/guides/first-install.md).

On versions that include remote-node onboarding, additional VPN Nodes are attached from Manager: create a VPN Node, choose **Connect server**, and run the generated Agent bootstrap command on the target Ubuntu host. The bootstrap connects Agent first; RouteGate installs the selected protocol runtime later as a separate managed action.

See [Add a remote VPN Node](docs/guides/add-vpn-node.md) for the user-facing onboarding sequence and retry guidance.

In the canonical Hybrid layout, nginx/HTTPS owns TCP `443`; the recommended VLESS / Reality listener uses TCP `8443` to avoid the HTTPS listener conflict.

## Product principles

- **Self-hosted by design** — infrastructure and data remain under the operator's control.
- **Open source** — security-sensitive and operationally critical code remains inspectable and self-buildable.
- **Guided Workflow / Next Action First** — major screens communicate current state and expose the most logical next action.
- **Product model over implementation model** — administrators manage infrastructure concepts rather than internal service plumbing.
- **Operational safety** — VPN deployment follows render, validation, apply, health-check, and rollback boundaries.
- **Verified software delivery** — release integrity, provenance, preflight, backup, health proof, and recovery are explicit update gates.
- **Explicit capability contracts** — detecting a binary or recognizing a VPN client is not the same as supporting its complete lifecycle.
- **No silent compatibility downgrade** — connection success is not presented as routing-policy enforcement when a client cannot carry the required policy faithfully.
- **Control-plane isolation** — remote Agents have no direct database access and no general remote-shell authority.
- **Provider-independent integrations** — external communication belongs to the Delivery domain rather than being scattered through product logic.
- **No artificial product limits** — RouteGate is one open-source self-hosted product, not a restricted edition around closed critical features.
- **Protected brand** — source code is AGPLv3-or-later; the RouteGate name and official branding are governed separately.

## Current capabilities on `main`

### Control plane and node management

- PostgreSQL-backed Manager state and migrations;
- React/TypeScript Admin UI with English-first product language and Russian localization;
- secure single-use administrator activation at `/setup`;
- Management, VPN, and Hybrid deployment roles;
- local Hybrid-node registration and remote VPN Node bootstrap;
- short-lived registration tokens and persistent Agent credentials;
- heartbeat, compatibility, capability, version, runtime, telemetry, and VPN-user presence inventory;
- named online VPN-user visibility across managed protocols, grouped by account/server/node;
- state-aware Dashboard and operational next-action guidance;
- responsive operator workflows and bounded list/history presentation for larger installations;
- allow-listed Agent infrastructure operations rather than arbitrary command execution.

### VPN protocols and configuration lifecycle

- explicit Manager/Agent adapter contracts;
- VLESS / Reality, WireGuard, Hysteria2, Shadowsocks 2022, and MTProto / FakeTLS managed protocol families;
- per-node default protocol plus per-client-profile `Auto` or explicit protocol preference;
- multi-runtime rendering and apply on one VPN node;
- render, validate, apply, health-check, rollback, and configuration history boundaries;
- server/account credential lifecycle and protected client-access rendering;
- certificate observation and recovery tooling for the Manager HTTPS path;
- protocol-specific certificate ownership where required.

### VPN accounts, subscriptions, and client access

- scalable VPN-account lifecycle, search, filtering, and management;
- Access & Devices: multiple devices per account, each with its own opaque subscription link and independent revoke/rotate lifecycle;
- persistent client profiles with explicit client identity;
- account-level protocol selection;
- QR codes, share/client representations, and subscription access;
- opaque HTTPS subscription credentials with hash-only storage, rotation, revocation, and optional expiry;
- capability-aware delivery for Hiddify (recommended), sing-box, v2rayN, v2rayNG, and a Generic best-effort fallback for other clients;
- client-specific routing representations and import helpers where the target client supports them;
- compatibility state and limitations surfaced instead of silently falling back to ordinary connectivity;
- User Portal and self-service foundations;
- traffic collection, visibility, limits, and enforcement foundations.

### Routing and node selection

- routing profiles with Direct, VPN, and Block actions;
- account-level routing-profile overrides with explicit inheritance;
- client-side serialization of the resolved Routing Profile without duplicating routing-policy logic;
- node groups with priority/weight membership;
- candidate health derived from role, Agent state, heartbeat freshness, protocol capability, runtime state, and load;
- explainable Automatic Selection preview/apply with cooldown and optional degraded fallback;
- protocol-aware candidate evaluation for accounts with an explicit protocol override;
- explicit deployment-required result after moving an account between nodes.

Automatic Selection is intentionally operator-driven today. RouteGate does **not** silently move accounts in the background when health changes because safe unattended failover requires coordinated deployment across the previous and selected nodes.

### Updates, operations, and integrations

- deterministic release metadata and platform bundles for supported Linux architectures;
- strict release-manifest verification with artifact SHA-256 and expected database migration identity;
- GitHub Artifact Attestations / Sigstore provenance bound to the RouteGate repository and release workflow;
- role-aware backup, apply, migration validation, health-check, rollback, and recovery engine;
- durable Manager update jobs for preflight, discovery, verification, staging, apply, and rollback orchestration;
- explicit administrator-driven local update workflow in the Admin UI;
- durable ordered VPN-node rollout with one-node-at-a-time admission, proof-gated advancement, restart-safe state, and explicit controls;
- runtime Dashboard data, named online VPN-user presence, and operational visibility;
- audit-oriented state transitions and operator-facing history;
- recovery/status tooling with fixed allow-listed actions;
- Delivery domain for durable external communication requests, templates, retries, lifecycle, history, and provider adapters;
- Email/SMTP, Telegram Bot API, and WhatsApp Business Cloud API adapter boundaries in the integration architecture.

## Architecture

```text
Administrator / User
        │
        ▼
RouteGate Admin UI / User Portal
        │
        ▼
RouteGate Manager ───────────── PostgreSQL
        │
        ├── Nodes / Accounts / Protocols / Routing
        ├── Config lifecycle / Client delivery / Traffic / Audit
        ├── Release verification / Updates / Rollouts
        │
        └── authenticated Agent tasks + aggregated telemetry
                 │
         ┌───────┴────────┐
         ▼                ▼
RouteGate Agent      RouteGate Agent
    VPN Node A           VPN Node B
         │                   │
         ▼                   ▼
 managed adapter(s)     managed adapter(s)
         │                   │
         ├─ sing-box         ├─ sing-box
         ├─ WireGuard        ├─ WireGuard
         ├─ Hysteria         ├─ Hysteria
         └─ mtg              └─ mtg
                 │
                 ▼
              VPN clients
```

The Manager follows a **modular monolith** architecture. Domain boundaries are kept explicit, but components should become separately deployed services only when real scaling or operational pressure justifies that complexity.

### Delivery / external integrations

The integration boundary is:

```text
RouteGate Manager
      │
      ▼
Delivery Service
      │
      ▼
Provider Adapter
      │
      ├── SMTP → Email
      ├── Telegram Bot API → Telegram
      └── WhatsApp Business Cloud API → WhatsApp
```

Delivery owns durable requests, template selection, idempotency, lifecycle/status, bounded retries, restart-safe recovery, and audit/history. Provider adapters contain external-system communication logic rather than leaking provider details into core product domains.

## Technology stack

| Area | Technology |
|---|---|
| Manager | Go |
| Agent | Go |
| Admin UI / User Portal | React, TypeScript, Vite |
| Database | PostgreSQL |
| Managed VPN runtimes on `main` | sing-box, native WireGuard, Hysteria, mtg |
| Release provenance | GitHub Artifact Attestations / Sigstore |
| Production-like validation baseline | VLESS / Reality on Ubuntu 24.04 LTS |
| Clean-host deployment | native systemd services, PostgreSQL, nginx, Let's Encrypt TLS |
| Development | Docker Compose |
| Public website | React/Vite static site on GitHub Pages |

## Repository layout

```text
agent/       RouteGate Agent
backend/     RouteGate Manager API and database migrations
brand/       Official brand assets and design tokens
deploy/      Development and deployment resources
docs/        Architecture, API, operations, features, decisions, and release notes
frontend/    RouteGate Admin UI and User Portal
scripts/     Development, release, update, packaging, and operational helpers
website/     Public routegate.org website
```

## Development quick start

Docker Compose is the contributor/development path; it is not the supported public clean-host installation contract.

### Requirements

- Git
- Go
- Node.js and npm
- Docker with Docker Compose
- GNU Make

```bash
git clone https://github.com/routegate/RouteGate.git
cd RouteGate
make dev
```

Open the Admin UI at `http://127.0.0.1:5173`.

Run project checks:

```bash
make check
```

Useful commands:

```bash
make help
make dev
make down
make restart
make logs
make ps
make backend-test
make agent-test
make frontend-build
make frontend-i18n-check
```

Development defaults are documented in [`.env.example`](.env.example). Never use example passwords or tokens for shared or production-like deployments.

## Documentation

Start here:

- [Documentation index](docs/)
- [Platform Expansion Architecture](docs/architecture/platform-expansion.md)
- [Updates, Releases, and Versioning](docs/architecture/versioning-and-updates.md)
- [Multi-Node Update Rollout](docs/architecture/multi-node-update-rollout.md)
- [Secure Config Delivery](docs/architecture/secure-config-delivery.md)
- [Client Capability-Aware Delivery](docs/architecture/client-capability-aware-delivery.md)
- [Client Compatibility Matrix](docs/architecture/client-compatibility-matrix.md)
- [Clean VPS Installer](docs/deployment/clean-vps-installer.md)
- [Remote VPN Node](docs/deployment/remote-vpn-node.md)
- [MVP Deployment Baseline](docs/deployment/mvp-deployment-baseline.md)
- [API documentation](docs/api/)
- [Feature documentation](docs/features/)
- [Architecture decisions](docs/decisions/)
- [Release documentation](docs/release/)
- [v0.1.0 release notes](docs/release/v0.1.0.md)
- [Official website](https://routegate.org)

Documentation is English-first, with Russian localization developed alongside the product.

## Current boundaries

RouteGate is not:

- an OPNsense plugin;
- a firewall distribution;
- a consumer VPN service;
- a closed-source VPN panel;
- a restriction-bypass marketing product.

The current project does not promise Kubernetes/HA orchestration, an appliance operating system, official RouteGate mobile clients, or identical production-like validation across every managed protocol, VPN client, and topology.

Automatic Selection is currently an explainable preview/apply workflow, not an unattended health-triggered failover service. Cross-node atomic VPN deployment, session draining, and live connection migration remain separate future problems.

RouteGate platform updates are administrator-triggered. Release-channel policy and unattended automatic updates remain future work rather than hidden behavior.

Client compatibility is explicit. RouteGate does not claim full Smart Routing merely because a third-party client can connect, and client-local TUN, DNS, routing-mode, or rule-precedence behavior may still require configuration for some clients.

## Open source and commercial services

RouteGate is one open-source self-hosted product. Critical infrastructure behavior is not intended to be hidden behind artificial server-count, account-count, administrator-count, or edition limits.

Commercial value may be offered separately through services such as official builds, support, deployment assistance, consulting, appliance packaging, enterprise services, or a future managed RouteGate offering without turning security-sensitive core behavior into closed code.

**Open code builds trust. Official operation creates commercial value.**

## Security

Please do not report security vulnerabilities through public issues. Read [SECURITY.md](SECURITY.md) for the supported reporting process and disclosure expectations.

## Contributing

Contributions, testing, documentation improvements, and carefully scoped proposals are welcome. Before opening a pull request, read [CONTRIBUTING.md](CONTRIBUTING.md), [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), and [SECURITY.md](SECURITY.md).

## License and trademarks

RouteGate source code is licensed under the [GNU Affero General Public License v3.0 or later](LICENSE).

The RouteGate name, logo, domain, official brand assets, and official-build designation are not granted by the software license. See [TRADEMARKS.md](TRADEMARKS.md) and [NOTICE](NOTICE) for the project boundary.

Unofficial builds and forks must not present themselves as official RouteGate releases.

**Open code. Protected brand. Trusted official builds.**
