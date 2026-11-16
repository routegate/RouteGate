# RouteGate Clean VPS Installer

## Status

- Public baseline: historical v0.1.0 MVP; newer v0.2.0 installer candidate is not yet publicly released
- Supported live installation target: Ubuntu 24.04 LTS on amd64
- Deployment model: native systemd services on one VPS
- Historical validation: disposable v0.1.0 production-like host; v0.2.0 public VPS acceptance is pending
- VPN Core: sing-box, installed after first login through RouteGate

The Clean VPS Installer is the canonical public installation boundary for the RouteGate MVP. The operator supplies a supported Ubuntu host, DNS, and working SSH/sudo access. RouteGate owns the management-platform installation from that point forward.

### VPN runtime ownership

The Clean VPS Installer installs the RouteGate platform and local Agent, but it
does **not** preinstall protocol runtimes.

After the first administrator signs in, RouteGate installs the runtime required
by the selected protocol through the same allow-listed Manager → Agent
operations used by remote VPN Nodes:

- VLESS / Shadowsocks → sing-box;
- WireGuard → native `wireguard-tools` plus RouteGate-managed forwarding prerequisites;
- Hysteria2 → checksum-verified Hysteria;
- MTProto → checksum-verified mtg.

This gives one runtime installation owner for Hybrid and remote VPN Nodes. A
runtime is installed only when it is needed, remains unconfigured/inactive
until validated Config Deploy, and is not part of the platform bootstrap
success boundary.

The Hybrid nginx configuration still contains the Hysteria2 ACME challenge
bridge required if Hysteria2 is selected later. Production-like updates
reconcile that bridge atomically and validate nginx before reload.

## Canonical product flow

```text
Clean Ubuntu 24.04 LTS VPS
        ↓
RouteGate-approved APT repository trust preflight
        ↓
one copy-paste installer command
        ↓
host, DNS, and conflict preflight
        ↓
verified RouteGate release bundle
        ↓
PostgreSQL + Manager + Admin UI + nginx/HTTPS + local Agent
        ↓
single-use /setup administrator activation
        ↓
Guided Workflow / Next Action First
        ↓
Choose a VPN protocol
        ↓
RouteGate installs the required VPN runtime through Agent
        ↓
recommended protocol configuration
        ↓
first VPN account
        ↓
Config Deploy: render → validate → apply → start/restart → health check
        ↓
persistent client profile / QR / VLESS link
```

The operator does not manually install PostgreSQL, copy migrations, assemble systemd units, configure nginx, or register the local Agent.

The installer intentionally does **not** install or start any VPN runtime.
VPN Core installation remains a deliberate, allow-listed post-login action.
The first successful Config Deploy owns real startup with a validated generated
configuration.

## Requirements

Before running the installer:

- use a clean Ubuntu 24.04 LTS amd64 VPS;
- require active APT sources to stay within the RouteGate-approved repository boundary: Canonical Ubuntu archive/security infrastructure (`archive.ubuntu.com`, regional `*.archive.ubuntu.com`, `security.ubuntu.com`, or `ports.ubuntu.com`) plus the explicitly approved Yandex mirror paths listed below;
- treat other provider-controlled, organization-controlled, PPA, and third-party APT sources as outside the default clean-host trust boundary until the operator deliberately replaces, removes, or explicitly approves them;
- connect as `root` or a user with working `sudo`;
- create a DNS `A` record for the chosen FQDN pointing directly to the VPS public IPv4 address;
- ensure inbound TCP ports 80 and 443 are reachable;
- preserve a working SSH session during the first installation;
- review whether the host already operates unrelated PostgreSQL, nginx, Apache, or another service on ports 80/443.

Already installed compatible APT packages are not conflicts. Active database/web services or unowned RouteGate files are treated as potential conflicts because they may contain unrelated data or configuration. The installer stops before unsafe mutation and presents safe recovery guidance.

The installer does not provision the VPS and does not modify SSH authentication policy.

### APT repository trust preflight

A "clean Ubuntu VPS" is a trust statement, not only an operating-system version check.
Before the installer performs any APT network operation, RouteGate enumerates active
`/etc/apt/sources.list`, `*.list`, and deb822 `*.sources` entries.

The default installation boundary accepts Canonical Ubuntu archive/security
hosts and these explicitly approved mirror paths:

- `mirror.yandex.ru/ubuntu` over HTTP or HTTPS;
- `mirror.yandex.ru/mirrors/download.docker.com/linux/ubuntu` over HTTP or HTTPS.

Only those Yandex paths are approved; arbitrary repositories on
`mirror.yandex.ru` remain outside the trust boundary. Any other active provider
mirror, PPA, private mirror, or third-party APT source causes a hard stop and is
printed as `[blocked]` for operator review. Explicitly disabled deb822 stanzas
(`Enabled: no`) are ignored.

RouteGate deliberately does **not** auto-rewrite repository configuration. The
operator must decide whether to replace, remove, or otherwise trust a source before
retrying installation. This keeps package provenance outside RouteGate's silent
mutation surface and prevents a hosting image from implicitly extending the
project's supply-chain trust boundary.


## Install RouteGate — new-host path

**Do not use the historical `v0.1.0` command examples for current-feature
installations.** The published `v0.1.0` MVP is not the newer Hybrid/Grafana
release, and mixing it with the current installer is incompatible.

The supported new-user starting point is
[routegate.org — Quick Start](https://routegate.org/#install).
When a reviewed **current stable release** with complete, verified GitHub
Release assets is published, its **Copy** button will yield one shell line:
download a pinned `install.sh`, verify it by SHA-256 **before root/sudo**,
and open the interactive installer configuration menu. The installer itself
verifies the matching release archive and checksum before any APT or service
changes.

**Until that current stable release is published:** do not run a copied
`v0.1.0` command from an older website, use `latest`/the moving `main`
branch, or guess a CI artifact URL. The actual release gate and external
clean-VPS acceptance are tracked in
[issue #715](https://github.com/routegate/RouteGate/issues/715).
See the task-oriented [first-install guide](../guides/first-install.md).

### Interactive numbered configuration (current installer candidate)

The new installer presents a **numbered SSH menu** on an interactive
`/dev/tty` before platform mutation:

- `1` — domain, Let's Encrypt contact, administrator email and server name;
- `2` — toggle RouteGate-managed Prometheus;
- `3` — select Grafana (which requires managed Prometheus);
- `4` — review the installation plan;
- `5` — continue to preflight, confirmation and installation;
- `0` — cancel before installing anything.

Unattended `--yes` preserves the noninteractive path; explicitly use
`--with-grafana` to request the add-on. Combining it with
`--without-prometheus` is rejected. Grafana is offered during initial
configuration, and its matching `install-grafana.sh` and
`INSTALLER_SHA256SUMS` are verified **before package and ownership-state
mutations**. A fully matched local release-candidate bundle may supply
these two sidecars alongside the bundle for disposable CI; arbitrary
custom bundle URLs cannot enable Grafana. Verified published installations
use exact-tag release assets.

When adding or retrying Grafana on a **completed** RouteGate host,
the installer rechecks the active APT repository trust boundary **before**
running a new Grafana package installation. An unapproved third-party APT
source blocks the add-on until the operator resolves it. Healthy
core-only reruns and verification of **already managed** Grafana remain
non-mutating and do not require the original clean-host APT sources; no
package operation is started on those idempotency paths. A failed prior
Grafana add-on may leave package/repository artifacts that require
reviewed recovery; the installer never silently takes over a foreign
Grafana package or repository.

If the core platform is interrupted, both its release tag/SHA-256
and its saved `GRAFANA_REQUESTED=0|1` choice are immutable for retry.
If selected Grafana fails **after core completion**, core remains
`STATUS=complete`; the installer exits unsuccessfully without printing
whole-install success. Retrying Grafana on the same pinned release checks
core health and does not reinstall Manager, Agent or PostgreSQL.
For completed hosts, the menu shows `INSTALLED` or `RETRY` when
appropriate; it does not act as a Grafana uninstall tool.

The installer is not a silent `curl | sudo bash` pipe and is not publicly
available while `v0.2.0` remains in review.

The public Manager requires a real DNS name and Let's Encrypt HTTPS.
After installer success, the first administrator uses the one-time `/setup`
link. The initial platform install does not silently install a VPN runtime:
choose and configure the protocol later through Manager → Agent.

### Historical release and expert verification

The older `v0.1.0` artifacts remain available in
[historical release notes](../release/v0.1.0.md) for maintaining those
deployments. They do **not** provide the newer installer menu or bundle
contract. Do not combine old and new assets.

For a reviewed, version-matched release candidate, the new installer also
provides a non-root `--verify-bundle` mode. It verifies the archive checksum,
safe extraction and manifest compatibility under a temporary directory and
does not install packages or write RouteGate ownership state. It is **not**
a public installation command while the candidate remains unpublished, and
a successful verification is **not** proof of a working Hybrid deployment.

CI also exercises a disposable Ubuntu 24.04 native Hybrid install with a
runner-local `.invalid` hostname and a temporary test certificate authority.
It tests actual service startup, Agent registration, one-use setup, VLESS
Reality traffic, interruption/retry, checksum failure and service restart.
**It does not prove** public DNS, Let's Encrypt issuance, an external VPN
client connection or a true host reboot. Those remain #715 acceptance gates.

Advanced reviewers of the new release may separately verify the
`install.sh` SHA-256, `INSTALLER_SHA256SUMS`, bundle checksums and
attestations from the **same pinned published tag**. These checks do not
replace public HTTPS and real-VPN-client acceptance on a disposable VPS.

## Installer options

```text
--domain FQDN          Public RouteGate hostname; prompted when omitted.
--email EMAIL          Let's Encrypt contact; prompted when omitted.
--admin-email EMAIL    Optional first administrator email.
--server-name NAME     Optional local All-in-One server display name.
--version VERSION      Exact reviewed release tag; pin it explicitly (avoid the mutable latest selector).
--verify-bundle        Non-root integrity/compatibility preflight only (candidate installer).
--with-prometheus     Include RouteGate-managed Prometheus.
--without-prometheus  Explicitly disable managed Prometheus.
--with-grafana        Include Grafana within the same verified installation run.
--without-grafana     Do not install Grafana (default).
--bundle-file PATH     Local release bundle for controlled E2E/offline staging.
--checksum-file PATH   Matching SHA256SUMS file for --bundle-file.
--bundle-url URL       Explicit HTTPS bundle URL.
--checksum-url URL     Matching HTTPS SHA256SUMS URL.
--yes                  Skip the final confirmation prompt.
--help                 Show command help.
```

Local-file and explicit-URL modes still require checksum verification. Explicit
remote artifact URLs must use HTTPS and cannot contain credentials, fragments,
or whitespace. A bundle without a matching SHA-256 entry is rejected.

## What the installer does

The installer performs these stages in order:

1. Presents the numbered menu on an interactive terminal (or processes explicit noninteractive arguments); validates the domain, email, component choices, Ubuntu version, architecture, systemd, required commands and privileges.
2. Enumerates active classic and deb822 APT sources and fails closed unless every active repository is inside the RouteGate-approved repository boundary. This happens before any `apt-get update` or package installation.
3. Shows which required APT dependencies will be reused and which will be installed.
4. Detects unowned RouteGate files, active web/database services, or listeners on TCP 80/443 before mutation.
5. Verifies that the FQDN resolves to an IPv4 address detected for the VPS.
6. Resolves the requested release, verifies the native bundle, `SHA256SUMS`, archive paths and manifest, and — when Grafana is selected — validates its pinned installer against `INSTALLER_SHA256SUMS` **before APT or RouteGate ownership-state writes**. A resumed installation must match the saved tag, bundle SHA-256 and Grafana choice.
7. Writes root-owned installation state with the resolved release tag and verified bundle SHA-256 (or verifies the existing pinned state before resuming).
8. Installs required platform APT packages including PostgreSQL, nginx, Certbot, curl, jq, OpenSSL, and Agent bootstrap utilities. Protocol-specific packages are deferred to the Agent runtime installation workflow.
9. Installs Manager, Agent, migrations, frontend assets, nginx configuration, and systemd units.
10. Creates a dedicated local PostgreSQL role/database with a generated password and loopback-only listening.
11. Generates a unique bootstrap administrator credential used only to initialize the first SuperAdmin and local platform workflow.
12. Starts Manager on `127.0.0.1:8080` and verifies its health endpoint.
13. Configures nginx, preserves the existing SSH firewall policy, and requests a Let's Encrypt certificate.
14. Enables `certbot.timer` and installs a RouteGate nginx validation/reload hook for certificate renewal.
15. Creates the local All-in-One Server through the authenticated Manager API.
16. Creates a one-time Agent registration token, starts the local Agent, and verifies persistent Agent credentials.
17. Creates a high-entropy, single-use administrator setup token and constructs `https://<domain>/setup#token=<token>`.
18. Removes bootstrap administrator values from the Manager environment and restarts Manager.
19. Writes root-only first-access/recovery information and installs `routegate-recovery`.
20. Verifies PostgreSQL, nginx, Manager, Agent, HTTPS health, Agent credentials, and local PostgreSQL exposure.
21. Marks the core installation state complete; if requested, installs and verifies Grafana. Only after all selected components succeed, prints the `/setup` next action. An optional Grafana failure reports a recoverable add-on error while leaving the core complete.

## First administrator activation

The canonical first access is the `/setup` link printed by the installer:

```text
https://vpn.example.com/setup#token=<single-use-token>
```

Security properties:

- the setup token is high entropy;
- only its SHA-256 hash is stored in PostgreSQL;
- the token is carried in the URL fragment so it is not sent as part of normal HTTP request URLs;
- the link is single-use;
- the link expires after 30 minutes;
- the administrator chooses and confirms a new password in the browser;
- successful activation consumes the token atomically, revokes bootstrap sessions, signs the administrator in, and removes the setup token from browser history.

The installer also writes root-only recovery information to:

```text
/root/routegate-first-login.txt
```

The file is mode `0600` and contains the setup URL plus a unique bootstrap password retained only as an emergency recovery credential if activation is not completed before the setup link expires. RouteGate does not email plaintext passwords and SMTP is not configured automatically.

After successful activation and password verification, remove the recovery file:

```bash
sudo rm -f /root/routegate-first-login.txt
```

## Guided first-run VPN setup

After `/setup`, RouteGate signs the administrator in and the Dashboard exposes the current setup state and exactly one primary next action.

The canonical All-in-One flow is:

1. confirm the automatically registered local Server/Agent is connected;
2. **Install sing-box** through the existing allow-listed Agent operation;
3. configure the recommended VLESS / Reality settings;
4. create the first VPN account;
5. run **Deploy VPN**, which uses the existing render → validate → apply → restart/start → health-check lifecycle;
6. open the account and use the persistent QR code or VLESS link in a compatible client.

Recommended All-in-One network ownership:

```text
nginx / RouteGate HTTPS    TCP 443
VLESS / Reality            TCP 8443
```

The recommended Reality flow uses TCP and `xtls-rprx-vision`, generates a fresh Reality keypair and Short ID, and requires an explicitly chosen external HTTPS handshake hostname. The node's own hostname is not automatically substituted. Check DNS and TLS connectivity to this destination from the VPN node; Manager validates the name's syntax but cannot establish that the node can reach it. Manual protocol settings remain available as an advanced workflow. Open the selected VPN listener port in the host and provider firewalls before client testing.

## State, logs, and retry behavior

Installer log:

```text
/var/log/routegate-installer.log
```

Installation ownership/state:

```text
/etc/routegate/install-state.env
```

Interrupted-install secrets are temporarily stored under:

```text
/var/lib/routegate-installer/
```

These files are root-only. When `STATUS=installing`, the installer resumes
only when the **verified release tag and SHA-256 equal the exact original
bundle**, in addition to the existing domain and Prometheus ownership checks.
Installation state records the resolved pinned `VERSION` (not the floating
`latest` selector) and `BUNDLE_SHA256` before packages or services change.
A changed release, replaced archive under the same tag, missing legacy
fingerprint or uncertain selector **blocks automatic recovery**.

Both the core installer and the managed Grafana add-on now stage updates to
`install-state.env` in **root-only temporary files in the same directory**
and atomically rename them into place. A failed write or rename does not
truncate the previous `installing` or `complete` record; only a fully
prepared replacement becomes visible. A fatal interruption can leave a
root-only staging file, which must never be treated as installation state.
Grafana's rollback restoration follows the same atomic replacement rule:
if it cannot replace the state, it preserves the original state and the
root-only pre-Grafana backup for manual recovery instead of deleting the
only available recovery copy.

This guards the identity of an interrupted or completed install; it is not
a substitute for database backups or physical-host acceptance.

Never erase partially installed PostgreSQL data, overwrite the state file or
bypass the guard to make a retry work. Preserve the root-only state/logs and
review the original version and artifact identity before manual recovery.
Same-bundle retries must also preserve the original `GRAFANA_REQUESTED`
choice. Missing/changed selections fail before package or service mutation.
After successful core verification, transient secrets are deleted and state
becomes `complete`. A failed post-core Grafana install can be retried on that
same pinned version without resetting the core.

When state is `complete`, running the installer again performs health/idempotency checks and exits without reinstalling the platform or rotating credentials.

For supported post-install certificate, service, and VPN config recovery, use:

```bash
sudo routegate-recovery status
```

See [RouteGate Recovery Tool](../operations/recovery-tool.md) for the fixed
operation allow-list and rollback behavior.

The installer refuses to overwrite partial RouteGate files that do not have valid RouteGate ownership state.

A completed Grafana installation records `GRAFANA_MANAGED=1` for accurate
idempotency and menu status. `INSTALLED` does not mean the installer's menu
can uninstall Grafana; `RETRY` means the add-on was requested but not
successfully managed. The disposable CI scenario uses local test TLS and
a synthetic domain, **not** a real public Let's Encrypt issuance.

## Release bundles

The v0.1.0 release workflow publishes:

```text
routegate-v0.1.0-linux-amd64.tar.gz
routegate-v0.1.0-linux-arm64.tar.gz
SHA256SUMS
```

Each bundle contains:

```text
bin/routegate-manager
bin/routegate-agent
manager/migrations/
frontend/
systemd/
nginx/
metadata/manifest.env
```

The release workflow verifies SHA-256 checksums and required bundle structure before publication.

Both amd64 and arm64 bundles are published to keep the native packaging contract multi-architecture. **The v0.1.0 Clean VPS installation support boundary is Ubuntu 24.04 LTS amd64**, because that is the architecture that completed the production-like clean-host E2E acceptance.

## Security boundaries

- Manager listens only on loopback and is exposed through nginx/HTTPS.
- PostgreSQL is local-only.
- The installer fails closed before APT network access when active package sources leave the RouteGate-approved repository boundary (Ubuntu archive/security or the two approved Yandex mirror paths above).
- RouteGate does not silently rewrite a host's APT sources; repository trust remains an explicit operator decision.
- Release checksum verification is mandatory.
- Archive traversal and archive links are rejected.
- Secrets are not passed as normal process command-line arguments.
- Sensitive installer files use mode `0600`.
- Existing SSH settings are not weakened.
- When UFW is already active, the installer adds only nginx HTTP/HTTPS access and preserves SSH rules.
- DNS mismatch or TLS failure stops the installation rather than presenting plaintext deployment as success.
- Existing unrelated web/database services trigger safe conflict handling and are never modified automatically.
- Agent infrastructure mutations are allow-listed rather than arbitrary shell execution exposed through Manager APIs.

## Explicit non-goals for v0.1.0

- operating-system installation or SSH hardening;
- Docker/Kubernetes/HA production deployment;
- external PostgreSQL deployment;
- automatic VPN Core installation during the platform installer;
- managed/automatic RouteGate update and rollback orchestration;
- appliance image work;
- additional VPN Cores or protocols;
- RG-101C client compatibility auto-tuning;
- destructive uninstall or cleanup of unrelated host software.

## Validated MVP result

The final clean-host acceptance on `us.routegate.org` validated:

- Clean Ubuntu 24.04 LTS → RouteGate installer;
- PostgreSQL + Manager + Admin UI + nginx/HTTPS + Agent startup;
- secure `/setup` activation;
- automatic local Agent registration;
- Guided Workflow;
- sing-box installation through RouteGate;
- VLESS / Reality configuration;
- first VPN account and Config Deploy;
- persistent client profile, QR, and VLESS link;
- real client connectivity with V2Box and V2RayTun;
- persistent `fingerprint=firefox` profile behavior;
- host reboot and automatic service recovery;
- working VPN connectivity after reboot.
