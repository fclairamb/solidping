---
model: sonnet
effort: low
---

# The command palette has no entries for the Organization and Account landing pages

## Problem

The dash0 command palette ([CommandMenu.tsx](web/dash0/src/components/CommandMenu.tsx))
lists several *sub-pages* of the organization and account sections — Members,
Invitations, Settings (`CommandMenu.tsx:68-70`) and Profile, API tokens, AI
assistants (`CommandMenu.tsx:58-66`) — but the two section landing pages
themselves have no entry:

- `/dash0/orgs/$org/organization`
- `/dash0/orgs/$org/account`

A user who opens the palette and types "organization" or "account" gets no
direct hit for the section itself — only the sub-page entries (whose titles
don't contain those words). Both parent routes exist and are valid navigation
targets: they redirect to a default child
([organization.index.tsx](web/dash0/src/routes/orgs/$org/organization.index.tsx)
→ invitations,
[account.index.tsx](web/dash0/src/routes/orgs/$org/account.index.tsx)
→ profile).

## Proposal

Add one palette entry per section to the `pages` array in
[CommandMenu.tsx:39](web/dash0/src/components/CommandMenu.tsx):

- **Organization** → `path: "/orgs/$org/organization"`, `group: "organization"`,
  placed first in its group (e.g. `Building2` icon).
- **Account** → `path: "/orgs/$org/account"`, `group: "account"`, placed first
  in its group (e.g. `CircleUser` icon — `User2` is already used by Profile).

Link to the **parent** path (not the current redirect target) so the palette
keeps working if the default child ever changes; the index routes' `redirect`
handles the rest, and `goTo` (`CommandMenu.tsx:134`) already navigates with
`params: { org }`.

Details:

- **i18n**: titles resolve through the `nav` namespace (`CommandMenu.tsx:88,141`).
  There is no top-level `organization` / `account` key yet (only `organizations`,
  plural, for the account's org list) — add both keys to all four locale files
  `web/dash0/src/locales/{en,fr,de,es}/nav.json`. Don't reuse
  `command.groupAccount` / `command.groupOrganization` for the item titles;
  those are the group headings rendered directly above the entries.
- **E2E**: extend [command-menu.spec.ts](web/dash0/e2e/command-menu.spec.ts) —
  searching "organization" / "account" surfaces the new entry, and selecting it
  lands on the redirect target (invitations / profile respectively). Give both
  entries a `testId` like the existing `command-menu-new-check` / `command-menu-ai`.
