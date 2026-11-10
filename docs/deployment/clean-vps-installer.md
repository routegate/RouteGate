# RouteGate Clean VPS Installer

## Status

- Release: RouteGate v0.1.0 MVP
- Supported live installation target: Ubuntu 24.04 LTS on amd64
- Deployment model: native systemd services on one VPS
- Validation environment: disposable production-like `us.routegate.org`
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


## Install RouteGate

> **Release parity notice (2026-11-11):** The public `v0.1.0` MVP is a historical
> stable release. It does **not** include the newer dark-only UI or the extensive
> `main` features. Always fetch `install.sh` from the **same immutable release
> tag** as the bundle requested via `--version`. Do **not** combine a current
> `main/install.sh` with `--version v0.1.0` or `latest`: current main requires
> updated bundle components absent from the old stable archive. The next
> current-version release and full clean-host Hybrid validation are tracked in
> [issue #715](https://github.com/routegate/RouteGate/issues/715).

The supported current-main installer verifies/downloads/extracts its entire
release bundle *before* installing platform packages or creating an install-state
ownership record. A failed bundle compatibility check must not leave an
ambiguous partial RouteGate installation. This does not replace a full clean-host
Hybrid deployment test.

### Interactive installation

Choose the release first. Download its installer to a file, inspect the exact
bytes that will run as root, then execute it:

```bash
VERSION=v0.1.0
curl -fL --proto '=https' --tlsv1.2 \
  "https://raw.githubusercontent.com/routegate/RouteGate/${VERSION}/install.sh" \
  -o routegate-install.sh
less routegate-install.sh
sudo bash routegate-install.sh --version "${VERSION}"
rm -f routegate-install.sh
```

Do not pipe a mutable branch such as `main` directly into `sudo bash`.

For releases produced by the current release workflow, the GitHub Release also
contains the installer scripts, `INSTALLER_SHA256SUMS`, and
`installer-scripts.attestation.json`. These artifacts let an operator verify
the installer separately before privileged execution.

The installer asks for:

- the public RouteGate FQDN;
- the Let's Encrypt contact email;
- whether the same email should be used for the first RouteGate administrator.

Pressing Enter accepts the recommended same-email choice.

By default, the installer resolves the latest published RouteGate release and verifies the selected bundle against the release `SHA256SUMS` file.

### Pin v0.1.0 explicitly

```bash
curl -fL --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/routegate/RouteGate/v0.1.0/install.sh \
  -o routegate-install-v0.1.0.sh
less routegate-install-v0.1.0.sh
sudo bash routegate-install-v0.1.0.sh \
  --domain vpn.example.com \
  --email owner@example.com \
  --version v0.1.0
rm -f routegate-install-v0.1.0.sh
```

### Unattended confirmation

Download and verify the installer first; `--yes` only skips RouteGate's final
interactive confirmation and does not weaken the bootstrap checks:

```bash
curl -fL --proto '=https' --tlsv1.2 \
  https://raw.githubusercontent.com/routegate/RouteGate/v0.1.0/install.sh \
  -o routegate-install-v0.1.0.sh
less routegate-install-v0.1.0.sh
sudo bash routegate-install-v0.1.0.sh \
  --domain vpn.example.com \
  --email owner@example.com \
  --version v0.1.0 \
  --yes
rm -f routegate-install-v0.1.0.sh
```

## Installer options

```text
--domain FQDN          Public RouteGate hostname; prompted when omitted.
--email EMAIL          Let's Encrypt contact; prompted when omitted.
--admin-email EMAIL    Optional first administrator email.
--server-name NAME     Optional local All-in-One server display name.
--version VERSION      Release tag; defaults to latest.
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

1. Prompts for missing domain/email values and validates arguments, Ubuntu version, architecture, systemd, required base commands, and privileges.
2. Enumerates active classic and deb822 APT sources and fails closed unless every active repository is inside the RouteGate-approved repository boundary. This happens before any `apt-get update` or package installation.
3. Shows which required APT dependencies will be reused and which will be installed.
4. Detects unowned RouteGate files, active web/database services, or listeners on TCP 80/443 before mutation.
5. Verifies that the FQDN resolves to an IPv4 address detected for the VPS.
6. Creates root-owned installation state and recovery storage.
7. Installs required platform APT packages including PostgreSQL, nginx, Certbot, curl, jq, OpenSSL, and Agent bootstrap utilities. Protocol-specific packages are deferred to the Agent runtime installation workflow.
8. Resolves the requested RouteGate release, downloads the native bundle, verifies `SHA256SUMS`, rejects unsafe archive paths/links, and validates the manifest.
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
21. Marks installation state complete and prints the `/setup` next action.

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

These files are root-only. If a RouteGate-owned installation is marked `installing`, re-running the same command for the same domain resumes with preserved installer secrets. After successful verification, transient secrets are deleted and state becomes `complete`.

When state is `complete`, running the installer again performs health/idempotency checks and exits without reinstalling the platform or rotating credentials.

For supported post-install certificate, service, and VPN config recovery, use:

```bash
sudo routegate-recovery status
```

See [RouteGate Recovery Tool](../operations/recovery-tool.md) for the fixed
operation allow-list and rollback behavior.

The installer refuses to overwrite partial RouteGate files that do not have valid RouteGate ownership state.

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
