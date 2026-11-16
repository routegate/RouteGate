# Install RouteGate on a new VPS

This guide is for an administrator who is installing RouteGate for the first time.

RouteGate's standard first-server layout is a **Hybrid Node**: the same VPS hosts the management plane and can also host VPN services.

## Before you start

Prepare:

- a clean Ubuntu 24.04 LTS VPS on amd64;
- root access or a user with working `sudo`;
- a stable public DNS name you control, such as `routegate.example.com` (a subdomain is sufficient and must be set up before installation);
- a DNS `A` record pointing that name to the VPS public IPv4 address;
- inbound TCP ports 80 and 443 reachable from the Internet;
- a working SSH session that you can keep open during installation.

Do not manually install PostgreSQL, nginx, RouteGate Agent, or VPN protocol runtimes first. The installer and RouteGate workflow own those components.

The public DNS name is required for the Manager. RouteGate does not support an IP-only Manager installation. An additional remote VPN Node does not need its own DNS name merely to connect its Agent to this Manager.

## 1. Copy the verified Quick Start command

Open [routegate.org — Quick Start](https://routegate.org/#install). Once
a new reviewed stable RouteGate release is actually published and the site
offers a verified command, click **Copy** and paste that **single line**
into the SSH terminal on your clean VPS.

The copied command pins an exact published release and verifies the
downloaded `install.sh` SHA-256 digest **before** running it with
administrator privileges. The installer then checks the matching release
bundle before installing packages or changing services.

**Current release warning:** the historical published `v0.1.0` does not
contain the newer `v0.2.0` feature set. If the website still offers
`v0.1.0` or marks Quick Start unavailable, **do not paste an old command**,
substitute a `main` branch installer, or mix installers and bundles from
different versions. See [release readiness #715](https://github.com/routegate/RouteGate/issues/715).
A first-install command will be enabled only after a verified current stable
release is approved.

## 2. Follow the installer wizard

Once Quick Start is enabled, the terminal installer guides the public
hostname, Let's Encrypt contact email, administrator email and optional
Prometheus/Grafana selection **during setup**. It then validates DNS,
host/service conflicts, APT trust, release compatibility and HTTPS
prerequisites before the install begins.

No `git clone`, `less`, CI ZIP download, manual release archive, or local
build is needed for the ordinary first-install path.

### Expected result

The installer should finish with:

- PostgreSQL configured locally;
- RouteGate Manager running;
- RouteGate Agent running;
- Admin UI available through nginx/HTTPS;
- a single-use `/setup` URL;
- installer state marked complete.

If installation stops, follow the failure message before making manual changes. The installer log is:

```text
/var/log/routegate-installer.log
```

The recovery status command is:

```bash
sudo routegate-recovery status
```

## 3. Create the first administrator

Open the single-use `/setup` URL printed by the installer.

Choose the administrator password and finish activation. The setup link is intentionally single-use.

### Expected result

You should reach the RouteGate Admin UI and see the local server as a **Hybrid Node** with a connected local Agent.

## 4. Choose the VPN protocol

The platform installer does not preinstall VPN runtimes.

Choose the protocol you want to configure. RouteGate then installs the required runtime through Agent as a managed action.

Examples:

- VLESS / Shadowsocks → sing-box;
- WireGuard → native WireGuard tools;
- Hysteria2 → Hysteria;
- MTProto → mtg.

Do not install those runtimes manually unless you are deliberately operating outside RouteGate's managed workflow.

## 5. Apply the first VPN configuration

Follow the guided workflow in the Admin UI:

```text
choose protocol
→ install required runtime
→ configure protocol
→ render
→ validate
→ apply
→ health check
```

A successful runtime installation is not the same as a successful VPN deployment. RouteGate treats configuration apply and health verification as separate steps.

For automatic VLESS / Reality setup, provide an external HTTPS hostname that resolves and completes a TLS handshake from the VPN node. RouteGate does not substitute the node's own DNS name. A syntactically valid name is not proof of reachability; test it from the node before using it. If the chosen VPN port is 8443/TCP, allow inbound traffic in the host firewall and the provider's firewall, if present. The Clean VPS Installer opens HTTP/HTTPS for the Manager but does not automatically open every possible VPN protocol port.

## 6. Create the first VPN account

After the server configuration is healthy, create a VPN account and generate the client delivery/profile required for your client application.

## If something goes wrong

Use this order:

1. Read the error shown by RouteGate or the installer.
2. Check `sudo routegate-recovery status`.
3. Check `/var/log/routegate-installer.log`.
4. Avoid manually replacing RouteGate-owned files or services before the failure is understood.
5. If you need help, include the failed stage and sanitized log output. Never share private keys, administrator passwords, Agent bearer tokens, or registration tokens.

## Next

To add another VPN server to this RouteGate installation, use [Add a remote VPN Node](add-vpn-node.md).
