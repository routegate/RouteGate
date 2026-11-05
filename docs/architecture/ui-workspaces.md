# UI workspace patterns (RG-80)

For design token ownership and CSS consolidation, see
[RG-80A Design System foundation](ui-design-system.md).

## Scope

The account workspace establishes reusable interaction patterns for the admin UI.
Dashboard summaries are entry points into those workspaces. This document describes
implemented conventions; it does not introduce a separate component library.

## Navigation and hierarchy

- Use URL-backed workspace navigation for distinct domains: overview, access,
  routing, protocols, traffic and settings. Preserve list filters when navigating.
- Make the selected section visible on narrow screens. After route navigation,
  move focus to the workspace content without trapping keyboard navigation.
- Use Section for a coherent task with a heading, description and relevant actions.
  Reserve cards for meaningful entities or summaries rather than every field.
- Keep one clear primary action per task. Put secondary explanations and uncommon
  actions behind disclosure, while keeping errors and required steps visible.

## Selection and actions

Use a list and a focused inline detail view when selecting a device or protocol.
Keep the selected entity identifiable alongside its actions. Reset entity-specific
state when changing accounts; never reuse a credential or confirmation across accounts.

Native disclosure is sufficient for secondary actions today. A future Drawer or
ActionMenu must have a clear label, predictable keyboard behavior, focus restoration
and an explicit close mechanism. Neither is a new generic component in this slice.
Do not move a long, independently navigable workflow into a transient overlay.

Place rotation, revocation and deletion next to the affected entity. Explain the
consequence and require the existing confirmation before performing the mutation.
Default to concealed credentials and deliberate reveal/copy actions. Never place
secrets in navigation URLs, notifications or diagnostic output.

## Status and next action

Use `shared/ui/Notice` for actionable feedback. Its `tone` selects existing
`form-message` styling (info, success, warning or error); it does not imply urgency.
Static guidance and query errors default to `announcement="off"`. Use
`announcement="polite"` for operation confirmation/progress (`role="status"`),
and `announcement="assertive"` for a failed user operation (`role="alert"`).
Live notices are atomic. Keep feedback next to the affected task, with explicit
next-action links/buttons as children where needed; do not move focus to it or
hide it on a timer. Never place credentials in a notice.

Initial consumers are routing profiles, account settings and the server inventory.
Other existing messages remain candidates for gradual migration; this is not a
claim of application-wide adoption or assistive-technology certification.
`npm run test:ui` verifies rendered semantics, styling compatibility and child
actions. Browser integration verifies rejected routing writes retain the draft
for retry and account saves expose polite confirmation.

A failed operation should leave the form and
its unsaved input available for correction. Background status refresh must not
replace an in-progress edit.

Distinguish loading, unavailable and a successfully loaded empty result. A failed
request must not produce a numeric zero that looks authoritative. Keep connection
confidence intact: recent activity is not proof of an exact current connection.

Dashboard links should land on the object or domain represented by the summary.
Deployment is server-scoped; select a server first or open the server shown in the
recent deployment. A ready account opens access management. Navigation must not
implicitly install, apply, rotate or revoke anything.

### Summary and status contract

- A summary represents one object/domain with a visible label, a meaningful value
  or status, and a link to its detail domain. Navigation is read-only, not an
  implicit mutation. Do not nest buttons or links inside a summary link.
- Use shared `StatusBadge` for account, server and routing-profile states. It
  normalizes whitespace/case, retains existing badge classes and textual labels,
  and treats missing values as unknown, never healthy or online. Unknown future
  status tokens remain visible rather than being mapped to success.
- Domain adapters may supply a more precise label (for example server connection
  state); the badge does not determine connectivity from recent traffic or infer
  service health. Server VPN-core summaries remain unknown when the Agent is not
  online, regardless of cached capability data.
- Badges are static text, not live regions or interactive controls. Color is
  supplementary to the label. Use Notice for feedback from a user operation.
- Summary data must distinguish pending, failed, empty and known-zero results.
  A failed request must not silently fall back to a stale healthy value or zero.

The current account/server summary layouts are intentionally retained; they do
not need a generic card wrapper solely to share markup. Integration checks click
all seven overview cards plus the routing overview action, verify their domain
destinations, and reject API writes during that navigation. The 34-layout matrix
covers the shared badges in the supported dark theme at both viewport sizes.

### Contextual actions decision

Keep current scoped buttons for common actions and native disclosure for secondary
details. No generic ActionMenu is introduced yet: there is no accepted consumer
that needs a transient menu rather than visible actions. Do not hide the next
logical action or a required warning behind an overflow menu merely for symmetry.
Keep destructive confirmation tied to the named object. If a future screen needs
ActionMenu, implement and test focus return, keyboard navigation, Escape/close and
disabled/pending behavior with that real consumer before generalizing it.

## Responsive and accessibility checks

Use native links and buttons, visible keyboard focus and associated form labels.
Allow navigation and dense tables to scroll within their own container where
necessary; avoid horizontal page overflow. Check mobile and desktop layouts in
the dark theme, as well as empty/error states and unusually long names.

## Reference implementations

- Account workspace: `frontend/src/pages/vpn-accounts/`
- Shared workspace primitives: `frontend/src/shared/ui/`
- Dashboard summaries: `frontend/src/pages/dashboard/DashboardPage.tsx`
- Guided onboarding: `frontend/src/pages/dashboard/GettingStartedWidget.tsx`

Account, server and routing-profile workspaces are implemented as described below.
Shared-pattern adoption remains gradual; this does not claim full UI migration.

## Integration verification

`Workspace integration` runs Chromium against the real Manager and a disposable
PostgreSQL 16 database in GitHub Actions. It covers login, Dashboard navigation,
all account workspace routes, persisted account edits, device creation and mobile
layout in the dark theme. Run `npm run test:workspace-integration` from `frontend`
with the same isolated environment defined in the workflow. The script requires
`ROUTEGATE_E2E_ISOLATED=1`, a loopback database named `routegate_workspace_e2e`,
`ROUTEGATE_DATABASE_URL` and `ROUTEGATE_E2E_MANAGER` (the compiled binary path).

The job creates test records and does not use a deployed installation. It does not
start an Agent, install a VPN runtime or verify real client connectivity. Those
remain separate staging checks before merging or deployment.

## Server workspace foundation

Server details now use `/servers/:serverId/:section` with overview, connection,
services, routing, deployments and settings. The old server URL and unknown
sections resolve to overview while retaining query parameters and navigation state.
The header preserves identity, role and connection state across domains.

The VPN service panel and Agent guidance are passed as React content instead of
being inserted through document-wide DOM queries. Domain panels remain mounted
while hidden to preserve drafts and in-flight operation state; switching server
identity remounts the workspace so tokens and dialogs cannot follow another server.
Shared navigation styling now belongs to WorkspaceNav itself.

Configuration versions now use a selectable list and focused actions. Deployment
history keeps failures visible and puts stages/timestamps in disclosure. The server
inventory uses five grouped columns and labelled cards on narrow screens. These
changes were deployed in PR #463 and visually accepted by the owner.

## Routing profile workspace

Routing profiles use `/routing-profiles/:profileId/:section` with overview, rules
and settings. Legacy and unknown section URLs redirect to overview, retaining
query parameters. The profile list remains alongside the selected workspace on
wide screens, with persistent profile identity and default/custom status.

The rule editor opens deliberately after the rule list. Domain switches retain
unsaved settings and rule input; changing profile identity remounts local state.
Profile refreshes do not overwrite dirty settings, including refreshes after rule
operations. Pending writes disable conflicting form controls. Deletion is scoped
to the named profile or rule and requires confirmation; default profiles remain
protected. Creating a profile opens its rules; deleting it returns to the list.
Callbacks from unmounted workspaces do not redirect the newly selected profile.

The rule list selects a focused detail card with complete matcher values and scoped
actions. Selecting a rule is read-only; selection stays fixed while the editor is
open. Saving selects the returned rule; deleting the selection falls back to the
first remaining rule. The editor groups domains and IP networks, with keywords and
GeoSite/GeoIP tags under additional matchers. Existing additional values expand that
group when editing, and closing it never removes values from the save payload.
The existing matcher fields, rule priority/action semantics and APIs are retained.
Integration checks verify all six matcher arrays survive creating and editing a rule.

Workspace integration additionally verifies a routing rule created through the UI,
settings-draft preservation during that save, persisted profile edits, routing
navigation and mobile layout against Manager and PostgreSQL.

## Responsive regression matrix

The permanent browser script checks all six server domains, all six account
domains and all three routing-profile domains at 390px and 1440px widths in the
dark theme (30 layouts). It also opens and cancels the routing-rule editor,
with additional conditions collapsed and expanded (4 more layouts). Each case
checks document/body horizontal overflow and visibility of the active domain in
the navigation strip. Domain checks retain the selected entity's header.

These are geometry/navigation regressions against isolated test data, not pixel
snapshots or proof of Safari behavior. Real-device visual acceptance, long-name
stress cases and full loading/error-state coverage remain separate checks.

### Dark-only appearance

The Manager UI, login and user portal support only dark appearance. The HTML
document declares it before React starts, and native controls use `color-scheme:
dark`. There is no theme toggle, OS-theme listener or stored theme preference
reader. Legacy `routegate.admin.theme` values are ignored, not migrated into any
account setting. Light-only styles and translations have been removed; shared
portal layout rules from those files are retained in `rg-shell-cleanup.css`.
The admin-session browser suite checks an existing light preference with a light
OS scheme at 390px and 1440px, including reload, portal and login navigation.

Account settings draft regressions also cover tab navigation, changed remote
identity/notes followed by a lifecycle-triggered refetch, successful persistence,
and isolation when switching accounts. This does not claim draft persistence for
every other account form.

## Traffic settings drafts

The non-secret traffic panel, like account settings, stays mounted under the
account-keyed workspace while its domain is hidden. Changing account identity
remounts it; this is in-memory retention, not browser storage or reload recovery.
The traffic query remains enabled (no new polling) so successful writes can
refresh their data even if the user changes domains during saving.

Local edits protect all four limit fields from background query updates. Inputs
and submit are disabled during a save or when traffic data is unavailable.
Successful saving clears the dirty guard only after a successful refresh; a
failed refresh does not restore stale input. Rejected writes retain the draft
for retry, and editing clears old success/error feedback.

Integration covers all five other tabs, a changed remote limit followed by an
overview-triggered refetch, pending controls, rejected save/retry, persistence,
and account isolation. Device/delivery forms still
need their own review. Credential-bearing domains continue to unmount when left;
they are not retained as hidden panels by this change.

## Routing settings drafts

The routing panel also stays mounted in the account-keyed workspace. Separate
edit guards protect node placement, explicit profile, node group and automatic
selection policy. Saving one form clears only its guard after a successful
refetch; other drafts remain intact. All routing write controls are disabled
during any routing write. Changing account discards the local drafts.

Automatic-selection preview is enabled only in the visible routing domain and
still requires saved group/policy settings. Selecting tabs never applies a node
decision. Policy refresh also invalidates the account so an applied placement is
reflected in account context. Credential-bearing access domains remain
unmounted on exit.

Integration exercises all four routing forms, all five other tabs, changed remote
profile/policy, rejected profile write/retry, pending controls, sibling-save
isolation, persistence and switching accounts against disposable Manager data.

## Protocol selection drafts

The protocol panel stays mounted under the account-keyed workspace. Its owner
retains only the primary protocol and selected protocol names, in memory. No
client links, subscription tokens or credentials are added to browser storage.
Leaving the account or reloading discards this draft; changing account remounts
the owner. Profile/settings queries are enabled only in the protocol domain.

Background profile refreshes update the saved/active summary without replacing
an explicit selection. Failed save/deployment retains it for retry. Only
confirmed activation clears it. A per-account mutation key locks selection and
apply controls while deployment is running, including a temporary panel remount.
The existing saved-desired versus applied-active workflow is unchanged.

`npm run test:protocol-drafts-browser` exercises the real router at 390px and
1440px with isolated API fixtures: navigation, newer remote preferences, account
isolation, reload reset, rejected save, retry, pending controls and confirmed
activation. These fixtures do not deploy runtimes or prove live node behavior.
