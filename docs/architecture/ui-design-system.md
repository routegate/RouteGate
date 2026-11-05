# RG-80A — UI Kit / Design System foundation

Status: first consolidation slice; **not** a visual redesign or complete migration.

## Authority and packaging

- `/brand` is the **single source of truth** for the product identity. Read
  `brand/brand-guide/DESIGN_MANIFESTO.md`,
  `brand/codex/RouteGate_UI_Guidelines.md` and
  `brand/tokens/routegate-tokens.{json,css,ts}` before changing UI identity.
- `frontend/src/shared/brand-tokens.css` is a **byte-for-byte copy** of
  `brand/tokens/routegate-tokens.css`. It exists because the frontend
  Dockerfile builds with `frontend/` as its own context; importing a parent
  directory would break that standalone workflow.
- `frontend/src/main.tsx` loads this copy before legacy UI styles.
  `npm run test:design-system` runs in a **full repository checkout** and
  rejects divergence between CSS and JSON, a stale frontend copy, missing
  RG-80 identity aliases or incorrect import order. This is run by CI.
- When updating brand colors/typography/radii: edit the authoritative
  `/brand` files, copy the canonical CSS into the frontend mirror, run the
  design-system test, and visually review affected pages. Do not edit the
  mirror alone.

## Token ownership

Official identity colors, font and radii are `--rg-color-*`,
`--rg-font-sans` and `--rg-radius-*` from the brand contract.
The existing `--rg-*` variables in `rg80.css` remain as **compatibility
aliases**. The following now inherit the official identity values rather
than carrying independent hex literals:

| RG-80 alias | Brand token |
| --- | --- |
| `--rg-bg-deep` | `--rg-color-bg` |
| `--rg-bg-main` | `--rg-color-surface` |
| `--rg-primary` | `--rg-color-primary` |
| `--rg-success` | `--rg-color-success` |
| `--rg-warning` | `--rg-color-warning` |
| `--rg-danger` | `--rg-color-danger` |
| `--rg-text-primary` | `--rg-color-text` |
| `--rg-text-secondary` | `--rg-color-muted` |

Some RG-80-only raised surfaces, borders, hover colors and cyan accents
are intentionally left unchanged in this slice. They are **not** new
approved brand colors; they require a visual migration decision.
The semantic success/danger alias values now use the official brand
palette, so these two states require visual and contrast acceptance.

## Existing component contracts

Reuse `frontend/src/shared/ui/` before introducing a new component.
Current primitives include `Section`, `Notice`, `StatusBadge`,
`EmptyState` and `WorkspaceNav`; they are not a complete generic UI
library. A generic primitive must have repeatable real consumers and
defined keyboard, focus, loading, error and disabled behavior.

- Prefer one visible **next action** for each task; make navigation a
  read-only operation. Keep a long workflow in a URL-backed workspace.
- Use `Notice` for action feedback. Static warning styling is not a live
  alert; only announce user-operation outcomes with the appropriate
  accessibility semantics.
- Never infer connection or service health from a badge color. An
  unavailable request is not a known zero; unknown must remain unknown.
- For destructive/credential actions, scope the target, expose the
  consequence and require explicit confirmation. Conceal secrets by
  default; never put them in a URL or status message.
- Enforce role, authorization and operation validity in the application
  and backend. CSS must not be used as an access-control boundary.

The Manager UI, login and portal are **dark-only**. Do not restore light
mode, theme toggles or OS-driven theme switching.

## Legacy layers and deferred work

Existing CSS entrypoint layers (`styles.css`, `rg80*.css`, `rg-brand.css`
and later feature-specific overrides) are retained to avoid a sweeping
specificity/regression change. The obsolete RG-80 gradient on the brand
mark is removed; the canonical brand lockup remains controlled by
`rg-brand.css`. New UI must not add contradictory identity declarations.

The following require focused follow-ups rather than speculative
global replacements:

1. Inventory overlapping button, card, form, navigation and status selectors;
   migrate only after inspecting real consumers and browser states.
2. Review `rg-spacing-audit.css` `:has(.badge-not-applicable)` selectors
   that conditionally conceal server UI based on a rendered status. Confirm
   role-aware **React rendering** and backend authorization before removing
   any legacy fallback. Do not expose actions by deleting CSS alone.
3. Review the `:has(.badge-offline)` headline workaround; derive text from
   actual connection state rather than styling a stale headline away.
4. Reconcile existing gradients, glow, cyan accents and shared status
   treatments against the Brand Guide, based on visual acceptance.
5. Complete keyboard/assistive-technology checks beyond existing geometric
   and component-rendering regression tests.

## Verification and acceptance

In a full checkout, from `frontend/`:

```bash
npm ci
npm run test:design-system
npm run test:ui
npm run build
```

CI also runs existing integration/browser suites. This slice does **not**
claim a production deployment, real-device certification or a complete
visual review. Before merge, visually compare 390px and 1440px layouts
in dark mode for login, Dashboard, server details, account workspaces
and portal, especially success/error badges, typography, background
and official logo. Recheck keyboard focus and disabled actions.

No Manager, Agent, live credentials, VPN node, database or deployment
configuration should be changed for RG-80A.
