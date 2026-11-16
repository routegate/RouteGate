# Release Readiness Checklist — historical v0.1.0

> **Historical record, not the current release gate.** This checklist captures
> the original `v0.1.0` MVP acceptance only; it must not be used to authorize
> `v0.2.0` publication or a new-host installation. Current installation
> readiness is tracked in [issue #715](https://github.com/routegate/RouteGate/issues/715).
> The current user-facing entrypoint is
> [routegate.org — Quick Start](https://routegate.org/#install), which must
> remain unavailable until a verified current stable release is published.

## RouteGate v0.1.0 MVP

This checklist defines the final release gate for the first public RouteGate MVP. It evaluates the supported product contract, not speculative future features.

A defect blocks v0.1.0 only when it breaks the supported MVP installation/operation path or makes that path unsafe or unusable.

## Release boundary

Supported live path:

- Ubuntu 24.04 LTS;
- amd64 Clean VPS installation;
- native systemd deployment;
- PostgreSQL + Manager + Admin UI + nginx/HTTPS + local Agent;
- secure `/setup` administrator activation;
- guided sing-box installation;
- VLESS / Reality;
- first VPN account;
- Config Deploy;
- persistent client profile / QR / VLESS link;
- real third-party client connectivity.

Published release packaging also includes an arm64 native bundle, but arm64 is not part of the v0.1.0 live Clean VPS acceptance boundary.

## Explicit non-blocking Post-MVP work

Do not delay v0.1.0 for:

- RG-101C client compatibility auto-tuning / advanced profile controls;
- additional VPN protocols or VPN Cores;
- appliance work;
- Kubernetes or HA;
- managed/automatic update orchestration;
- official mobile applications;
- opportunistic redesign or refactoring.

## Source-of-truth checks

Before tagging:

- [ ] `main` is the only active release branch.
- [ ] the release candidate commit is known and recorded.
- [ ] no unintended runtime changes were introduced after the final Clean VPS E2E acceptance.
- [ ] all intended release-closeout documentation changes are merged.
- [ ] required CI on final `main` is green.
- [ ] no release-blocking issue remains open.
- [ ] RG-101C remains explicitly Post-MVP / Planned.

## Automated checks

Run or verify the repository CI equivalent of:

```bash
make check
```

Required areas include:

- backend tests;
- Agent tests;
- frontend localization validation and production build;
- website build;
- installer Bash syntax;
- ShellCheck;
- installer TAP tests;
- native release-bundle build/structure/checksum validation.

Do not publish the tag while required checks are failing.

## Clean VPS acceptance

The release candidate must preserve the already validated lifecycle:

- [x] clean Ubuntu 24.04 LTS host;
- [x] installer owns PostgreSQL, Manager, frontend, nginx/HTTPS, and local Agent setup;
- [x] installer creates/registrations the local All-in-One Server/Agent automatically;
- [x] `/setup` activation is single-use and works end to end;
- [x] the administrator lands in a state-aware Guided Workflow;
- [x] sing-box can be installed through the allow-listed Agent workflow;
- [x] installed-but-unconfigured sing-box is not treated as VPN-ready;
- [x] recommended VLESS / Reality configuration works with nginx on 443 and VLESS on 8443;
- [x] first VPN account can be created;
- [x] Config Deploy renders, validates, applies, starts/restarts, and health-checks the runtime;
- [x] persistent client profile is exposed as QR and VLESS link;
- [x] V2Box connectivity works;
- [x] V2RayTun connectivity works;
- [x] working `fingerprint=firefox` profile persists;
- [x] host reboot restores required services;
- [x] VPN connectivity works after reboot.

## Security checks

Confirm the public path still preserves these boundaries:

- [ ] release bundle checksum verification is mandatory;
- [ ] unsafe archive paths/links are rejected;
- [ ] Manager is loopback-only behind nginx;
- [ ] PostgreSQL is not exposed on a public wildcard listener;
- [ ] HTTPS is required for a successful public installation;
- [ ] unrelated active host services are not silently overwritten;
- [ ] bootstrap administrator environment values are removed after platform bootstrap;
- [ ] `/setup` token is single-use and time-limited;
- [ ] root-only recovery material uses restrictive permissions;
- [ ] Agent infrastructure operations remain allow-listed rather than arbitrary remote shell execution;
- [ ] secrets/tokens are not included in release evidence or public logs.

## Documentation checks

Before tagging:

- [ ] README identifies v0.1.0 as the first public MVP release.
- [ ] README links the supported Clean VPS installation guide.
- [ ] deployment docs distinguish the public native installer from contributor Docker Compose.
- [ ] `/setup` is documented as the canonical first-access path.
- [ ] the `443` nginx / `8443` VLESS All-in-One boundary is documented.
- [ ] release notes describe supported platforms, validated clients, and known boundaries.
- [ ] arm64 packaging is not misrepresented as live-validated installer support.
- [ ] no document presents the obsolete generated-password-only first login as the canonical path.
- [ ] no document presents the July Docker/Codespaces acceptance as the final production-like MVP acceptance.

## Tag and release checks

Create the official release only after final `main` is green.

Expected tag:

```text
v0.1.0
```

The tag must point to the final release-closeout `main` commit.

The tag push triggers `.github/workflows/release.yml`. The release is complete only when the workflow succeeds and the GitHub Release contains:

```text
routegate-v0.1.0-linux-amd64.tar.gz
routegate-v0.1.0-linux-arm64.tar.gz
SHA256SUMS
```

Verify:

- [ ] release workflow conclusion is `success`;
- [ ] all three assets are present;
- [ ] asset sizes are non-zero;
- [ ] `SHA256SUMS` contains both bundle filenames;
- [ ] `sha256sum -c SHA256SUMS` passes against the downloaded bundles;
- [ ] required bundle structure is present;
- [ ] release title is `RouteGate v0.1.0`;
- [ ] curated `docs/release/v0.1.0.md` notes are used.

## Post-release confirmation

After the GitHub Release exists:

- [ ] the default installer can resolve the latest release as v0.1.0;
- [ ] explicit `--version v0.1.0` points to the published assets;
- [ ] README and routegate.org public deployment copy match the released contract;
- [ ] RG-01A records `MVP Complete / RouteGate v0.1.0 Released`.

## Final decision

Release status can be declared:

```text
RouteGate MVP Complete
RouteGate v0.1.0 Released
```

only after the tag-triggered release workflow and published asset verification pass.
