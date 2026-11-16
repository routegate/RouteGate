# RouteGate Website

This directory contains the source code for the public RouteGate website at `routegate.org`.

The website is part of the project repository for maintainability and coordinated releases. It is **not** required to install, deploy, or operate RouteGate Manager, Agent, or VPN Core.

## Stack

- Vite
- React
- TypeScript
- Static Git-managed content

## Development

```bash
cd website
npm install
npm run dev
```

## Production build

```bash
npm ci
npm run build
```

The static build is written to `website/dist/`.

## GitHub Pages deployment

The repository includes `.github/workflows/pages.yml`. It builds this directory
and deploys `website/dist/` through GitHub Pages when website-related changes
reach `main`. The workflow can also be started manually.

The production URL is `https://routegate.org/`. Vite intentionally uses the root
base path (`/`) for this custom domain. `public/CNAME` is copied into every
production build.

### Current live-domain checks

The production domain is already configured; the original first-time Pages
and DNS cutover instructions are historical setup steps, **not** the
recommended current operational procedure. For current Pages source,
deployment status and custom-domain configuration, use the repository's
**Settings → Pages** and the
[official GitHub Pages documentation](https://docs.github.com/en/pages/configuring-a-custom-domain-for-your-github-pages-site).

Before and after any **explicitly approved** DNS/Pages change, verify the
live hostname and supporting public files:

```bash
curl -I https://routegate.org/
curl -I https://routegate.org/ru/
curl -I https://routegate.org/robots.txt
curl -I https://routegate.org/sitemap.xml
dig routegate.org
dig www.routegate.org
```

Do **not** use the old GitHub Pages repository path as an installation or
live-site availability check. It is not the canonical public URL.
DNS records and HTTPS settings must be checked against current GitHub Pages
requirements; do not blindly copy old IP addresses or change unrelated
mail/verification records. This repository does not automatically mutate DNS.

## Map data

The dashboard preview uses a static, vendored SVG derived from Natural Earth
Admin 0 Countries at 1:110m scale. Natural Earth data is public domain. See
`public/map-data-license.txt` for source details. The site makes no runtime map
requests.

## Search and social metadata

The static entry point includes canonical, Open Graph, Twitter Card, and
structured-data metadata. `public/og-image.svg` is the editable source for
`public/og-image.png`; update both when changing the social preview.

## Installation and public release accuracy

The installation section on `routegate.org` is a **release contract**. It
must never hardcode a historical `v0.1.0` first-install command or silently
use `main`, `latest` or unverified CI artifacts. For a new stable release,
Copy must provide **one** command for an exact tag and verify the published
installer SHA-256 before invoking it as root. If required published assets
are missing, Copy must remain disabled.

The public website must not claim `v0.2.0` is available while it remains
under review. Follow [installation readiness #715](https://github.com/routegate/RouteGate/issues/715)
and keep the root [README](../README.md) and
[first-install guide](../docs/guides/first-install.md) synchronized. Site
source changes do **not** mean GitHub Pages has been deployed.

Before any public promotion, validate a real browser Copy interaction and
confirm that the pasted command installs the same release on an external
clean VPS. Run the [documentation link audit](../scripts/check-documentation.mjs)
when changing site installation copy.

## Content and brand boundaries

- Public user-facing copy belongs in locale content files.
- Official RouteGate brand assets originate from `/brand`.
- Do not add secrets, production tokens, private analytics credentials, or real infrastructure data.
- The website presents RouteGate but does not define core architecture, commercial boundaries, or roadmap sequencing.
- Do not add production-readiness claims unless the product status explicitly supports them.
