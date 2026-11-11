# RouteGate release promotion gate

Status: **manual review required**. This gate applies to public stable tags as well as release candidates. The GitHub Release workflow creates a **draft**, not a publicly downloadable release. The public Quick Start must be independently checked against the **published** stable release. The legacy v0.1.0 MVP must not be offered as a default automatic install while the current release is unverified (see #720).

## Why

Successful build/tests do not prove that a fresh VPS installer, admin setup, nginx/Let's Encrypt, local Agent, PostgreSQL and the VPN runtime actually work together. During the November 2026 RU/NL recovery, a fresh Hybrid install failed and the publicly suggested v0.1.0 bundle omitted much of the current UI. This must not recur.

## Checklist before a reviewer publishes a draft

Record evidence, including exact artifact version and source commit, in the release notes or the tracking issue:

- [ ] Same-commit CI includes a **tagless, two-architecture release rehearsal** for genuine linux/amd64 and linux/arm64 archives: checksums, embedded commit/version, native binary ELF architecture, frontend parity and corruption rejection. This does not run ARM64 binaries or prove actual tag attestation/publication.
- [ ] GitHub ruleset/permissions prevent force-moving or deleting release tags `v*`. The release workflow re-reads the **live origin tag** at both source validation and immediately before draft creation, rejecting changed/deleted tags (including annotated tags). This extra check narrows the race but **does not replace protected tags**; an administrator must verify the ruleset before publication.
- [ ] Tag points to the expected reviewed commit, and CI for that exact commit is green. The release workflow's attested bundle, installer scripts, release manifest and both checksum sets verify. Do not publish if the version/commit/bundle mismatch.
- [ ] Installer from the **same release tag** is used with matching bundle, never mixed with `main` or `latest`. A deliberately stale bundle fails **before** packages, services and RouteGate installation state are changed.
- [ ] On disposable, fresh Ubuntu 24.04 amd64, a dedicated DNS name resolves to the test VPS, TCP 80/443 are reachable, and the exact candidate is installed as **Hybrid/All-in-One**, not as a remote-only node. Record redacted stage-by-stage logs.
- [ ] PostgreSQL migration, Manager, nginx HTTPS (valid public certificate), one-time `/setup` and first admin login pass. Validate published frontend is the intended version and has the dark-only appearance (no selectable Light theme).
- [ ] Local Agent enrollment/heartbeat and VPN runtime installation, check/apply and listener ownership pass. The admin HTTPS listener must not conflict with the VPN listener.
- [ ] A real external VPN client imports the subscription and establishes traffic via the candidate node. Verify refresh after a non-destructive configuration change, after a restart and after reboot; capture network failures and distinguish DNS/TLS/SSH from runtime bugs.
- [ ] RG-140 open work is reviewed separately: [#509](https://github.com/routegate/RouteGate/issues/509) subscription continuity, [#538](https://github.com/routegate/RouteGate/issues/538) readiness/FI 503 recovery, [#545](https://github.com/routegate/RouteGate/issues/545) verified credential revocation, plus transfer/apply safety regressions. **Do not imply verified revocation of cached VPN credentials by merely invalidating a subscription URL.** Block release on unresolved security-critical scenarios; document any other known limitations explicitly.
- [ ] Update and recovery paths are exercised against a disposable installation: backup/restore, failed update rollback, safe repeat install and troubleshooting instructions. No secret/token should appear in public CI logs or issue attachments.
- [ ] Public website, README and installer docs point to the **same verified tag** and accurately explain supported protocols. Update them only with the reviewed release promotion.

## Publishing and immutability

1. A tag matching `vX.Y.Z` or `vX.Y.Z-rc.N` must point to a commit in the canonical `main` history. The workflow refuses tags from unrelated/unreviewed branches. Every candidate must include `scripts/hybrid-disposable-installer-e2e.sh`, `scripts/hybrid-vpn-e2e.py`, `scripts/test-release-candidate-dry-run.sh`, and both CI jobs `hybrid-native-e2e` and `tagless-release-candidate`; missing coverage **blocks** release creation.
2. The release workflow invokes the repository's reusable `RouteGate CI` **on the exact tag commit**. Only if every CI job, including disposable native Hybrid installation and real isolated VLESS/Reality traffic, succeeds will the release workflow build, checksum, attest and create a **GitHub Release draft**.
3. Review the draft and all evidence. A green CI badge alone is **insufficient**; recording a real external Hybrid E2E is mandatory for the supported current release. The release reviewer explicitly decides whether RG-140 blocks promotion.
4. Publish the draft **manually** using the GitHub Releases interface only after the checks above. For release candidates, retain the prerelease designation. Publishing is intentionally a separate operator action, not part of CI.
5. Never replace release assets or re-point an existing published tag. If anything changes, cut a **new** version and regenerate artifacts/attestations. The workflow refuses to upload into an existing draft or release.
## Manual release-artifact rehearsal

`workflow_dispatch` is **not** a way to publish a release. It may build and
attest development artifacts only when started from the **current remote
`main` HEAD**, using an explicit label `dev` or `dev-...` (bounded,
lowercase, filename-safe). The source commit and label are validated before CI,
again before bundling and once more before persisting attested artifacts.
Starting manually from a feature branch, using a stale main SHA, or supplying
a published-looking `vX.Y.Z` label is intentionally blocked. For an actual
versioned candidate, use the separately gated `v*` tag path; it creates only
a draft and requires external acceptance before public promotion.

## Operational boundaries

- The US production Manager, FI node, and NL candidate VPS are **not** CI test targets.
- NL is intended to become a self-contained **Hybrid** with its own Manager, database, Agent and VPN runtime, independent of US availability.
- No installation or update on US/FI/NL is authorized by this document. Use a disposable test VPS for initial acceptance.
- The original RU failure stage is still unverified until sanitized logs are available. Do not claim the installer issue is fully resolved just because the draft workflow is safe.

Tracking: [#715](https://github.com/routegate/RouteGate/issues/715) release/install reliability; [#716](https://github.com/routegate/RouteGate/pull/716) early installer parity preflight.
