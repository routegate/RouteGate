# RouteGate UI Guidelines for Codex

Before making any UI, UX or branding changes, read `/brand` and treat it as the single source of truth.

## Mandatory
- Use the official Controlled Passage with breaks logo.
- Do not use the old continuous-line logo.
- Use Inter for UI typography.
- Use design tokens from `/brand/tokens`.
- Keep the product dark-first.
- Use official colors only.
- Avoid gradients, glow, hacker/neon aesthetics, shields and locks as primary identity.

## Admin UI
- Primary logo: `/brand/logos/svg/logo-primary-horizontal-dark.svg`
- Compact sidebar/app mark: `/brand/logos/svg/symbol.svg`
- Favicon/app icon: `/brand/logos/svg/app-icon-dark.svg`

## Brand language
Main slogan: Control Your Routes.
Product description: Modern Self-Hosted VPN Management Platform.

## Design token implementation
- `/brand/tokens` is authoritative. The frontend uses a checked mirror at
  `frontend/src/shared/brand-tokens.css` to support standalone frontend builds.
- When the official palette changes, update the brand token sources and copy
  the CSS mirror. Run `npm run test:design-system` from `frontend/` in a
  complete repository checkout.
- Legacy RG-80 variables are compatibility aliases, not independent identity
  colors. See `docs/architecture/ui-design-system.md` for scope and exceptions.
