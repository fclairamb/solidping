---
model: sonnet
effort: medium
---

# The command palette's quick search cannot switch organization

Also covered here (same component, same locale files, same E2E spec): the
palette's "Settings" item should read "Organization Settings".

## Problem

The dash0 command palette (`⌘K`, [CommandMenu.tsx](../../web/dash0/src/components/CommandMenu.tsx))
lets you jump to pages, run quick actions, and fuzzy-find entities (checks, status
pages, escalation policies, SLOs) — but it has no way to switch to another
organization. A user who belongs to several orgs has to leave the palette, open the
sidebar org switcher or navigate to `/orgs/$org/account/organizations`, and switch
from there. For the keyboard-driven flow the palette is meant to serve, that's a
dead end: typing another org's name into the palette finds nothing.

Everything needed already exists:

- `useAuth()` exposes `organizations` (the user's memberships, with `slug`, name and
  `role`) and `switchOrg(orgSlug)` (`web/dash0/src/contexts/AuthContext.tsx:100`,
  `:470`) — already consumed by the sidebar switcher
  (`web/dash0/src/components/layout/AppSidebar.tsx:145`) and the organizations page
  (`web/dash0/src/routes/orgs/$org/account.organizations.index.tsx:26`).
- The palette already renders static groups (`actions`, `pages`, `account`,
  `organization` — `CommandMenu.tsx:35`, group order at `:105`) plus dynamic entity
  groups driven by the search input (`:149`–`:181`).

## Proposal

Add a "Switch organization" group to the command palette:

- List the user's *other* organizations (exclude the org currently in the URL,
  matching the sidebar's `organizations.find((o) => o.slug === org)` logic) as
  palette items — org display name as the label, slug as a secondary hint, with a
  suitable icon (e.g. `Building2`), searchable by both name and slug through the
  normal cmdk filtering.
- Selecting one calls `switchOrg(o.slug)` and then navigates to `/orgs/$org`
  (dashboard) under the new slug, mirroring what the sidebar switcher does
  (`AppSidebar.tsx:163`), then closes the palette. Handle a failed `switchOrg`
  the same way the organizations page does (toast with the API error, palette
  can stay closed).
- Placement: after the existing static groups (or as its own group at the end) —
  keep it out of the way when the user has a single org: **render nothing at all
  when there are no other organizations** (single-org users, the common case).
- Add i18n keys for the group heading (e.g. `command.groupSwitchOrg`) in **all**
  locale files, and a `data-testid` on the items for testing.
- E2E: extend the existing command-menu spec — with a second org created via the
  API, opening the palette and typing that org's name shows the item, and
  selecting it lands on `/orgs/<other>/` with the switch effective. Also assert a
  single-org user does not see the group.

### Rename the palette's "Settings" item to "Organization Settings"

The last item of the palette's `organization` group
(`web/dash0/src/components/CommandMenu.tsx:102`) uses `titleKey: "settings"`,
resolved against the `nav` namespace (`web/dash0/src/locales/en/nav.json:37`,
`"settings": "Settings"`). In search results the group heading gives little
context, and "Settings" is ambiguous with account-level settings.

- Do **not** rename the shared `nav:settings` key — it is also consumed by the
  organization section nav (`web/dash0/src/routes/orgs/$org/organization.tsx`),
  where the short label is correct in context.
- Instead give the palette item its own key (e.g. `command.organizationSettings`,
  next to the existing `command.*` keys in `nav.json`), translated in all four
  locales (`en`, `fr`, `de`, `es`): "Organization Settings" / equivalents.
- Keep it searchable by "settings" alone (the label contains the word, so cmdk
  substring matching already covers this).

Open question: whether switching should preserve the current page path under the
new org (like the settings page's slug-rename redirect) or always land on the
dashboard. Default to the dashboard — the sidebar switcher already does that, and
same-path-under-another-org frequently 404s (entities are org-scoped).
