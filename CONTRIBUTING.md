# Contributing to RouteGate

Thank you for helping improve RouteGate. This repository is the open source
home for the RouteGate Manager, Agent, Admin UI, deployment materials, and
project documentation.

## Project Boundary

Unless a file or directory says otherwise, contributions to this repository are
licensed as AGPL-3.0-or-later.

The RouteGate name, logos, icons, visual identity, and brand assets are not
licensed as open source software. See TRADEMARKS.md and brand asset license
files before reusing project identity materials.

## How to Contribute

1. Open an issue or discussion for larger behavior, architecture, security, or
   compatibility changes before doing substantial work.
2. Keep pull requests focused on one concern.
3. Include tests or documentation updates when behavior changes.
4. Run the relevant project checks before submitting a pull request.
5. Follow the CODE_OF_CONDUCT.md in all project spaces.

## Development Checks

Use the repository Makefile when possible:

```sh
make check
```

For narrower changes, run the relevant backend, agent, or frontend checks
documented in README.md and the project documentation.

## Documentation and release-link maintenance

Documentation is part of the release contract, not a post-release chore.
When changing the installer, onboarding flow, CLI arguments, published
assets, version labels or website Quick Start, update the root
[README](README.md), [first-install guide](docs/guides/first-install.md),
[Clean VPS reference](docs/deployment/clean-vps-installer.md) and applicable
website copy **in the same change**. Do not include a runnable command for an
unpublished tag, a historical release as the recommended install, or a moving
`main`/unverified `latest` installer.

Run the repository-wide offline link and installation-contract audit:

```sh
node scripts/check-documentation.mjs
```

The [Documentation consistency workflow](.github/workflows/documentation.yml)
checks all tracked Markdown/TXT files on PRs and on `main`. It verifies
relative paths and repository-owned GitHub URLs, plus the current-install
entrypoint. **External HTTP links are counted but not verified online**;
review external installation/download links and public site behavior manually
before promotion. Historic release notes remain historic; do not rewrite them
to describe a later release.

See [issue #715](https://github.com/routegate/RouteGate/issues/715) for the
current external VPS acceptance and safe Quick Start publication gate.

## Contribution Licensing

By submitting a contribution, you certify that you have the right to submit it
and agree that it may be distributed under the repository license. Unless you
clearly state otherwise in writing, intentionally submitted contributions are
provided under AGPL-3.0-or-later.

Do not submit code, documentation, assets, or data copied from another project
unless the license allows it and the attribution is preserved.

## Security Issues

Do not report suspected vulnerabilities in public issues with exploit details.
Follow SECURITY.md so maintainers can coordinate disclosure and remediation.
