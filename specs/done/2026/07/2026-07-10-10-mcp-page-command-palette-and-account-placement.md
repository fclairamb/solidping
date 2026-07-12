# MCP (AI) page: add command-palette shortcut and move it under Account

## Problem

Two discoverability/placement issues with the MCP (AI assistants) page:

1. **No command-palette entry.** The ⌘K palette
   (`web/dash0/src/components/CommandMenu.tsx:38-62`) lists dashboard, checks,
   incidents, …, profile, tokens, members — but not the MCP/AI page, so it
   cannot be reached from the palette at all.
2. **Wrong placement.** The page currently lives at the top level
   (`web/dash0/src/routes/orgs/$org/mcp.tsx`, sidebar entry "AI" with the
   `Bot` icon at `web/dash0/src/components/layout/AppSidebar.tsx:119-124`).
   It should live in the **Account** section at `/orgs/$org/account/mcp`,
   next to `/orgs/$org/account/tokens`. This matches the page's own history:
   it already moved out of Organization settings because "connecting an MCP
   client is user-level setup, not org configuration" (comment in
   `web/dash0/src/routes/orgs/$org/organization.ai.tsx`) — and user-level
   setup is exactly what the Account section (profile / security / sessions /
   tokens / notifications) holds.

## Proposal

### Move the page under Account

- Rename `routes/orgs/$org/mcp.tsx` → `routes/orgs/$org/account.mcp.tsx` and
  add a tab to the Account layout's `tabs` array
  (`web/dash0/src/routes/orgs/$org/account.tsx:13-19`), after Tokens.
- Keep `/orgs/$org/mcp` as a redirect to `/orgs/$org/account/mcp`, mirroring
  the existing `organization.ai.tsx` redirect pattern, so bookmarks and
  external links don't 404. Update `organization.ai.tsx` to redirect straight
  to the new path (avoid a redirect chain).
- Remove the now-dead `isMcp` special-case in the org layout breadcrumb logic
  (`web/dash0/src/routes/orgs/$org.tsx:140`) — the Account layout provides
  the header/tabs.
- Sidebar: drop the top-level "AI" entry from `AppSidebar.tsx` (the page is
  now reachable via the Account tabs and the new palette entry). If we'd
  rather keep sidebar visibility, point it at the new path instead — decide
  during implementation, but don't leave it pointing at the redirect.

### Command-palette shortcut

- Add an entry to the `pages` array in `CommandMenu.tsx`:
  `{ titleKey: "ai", path: "/orgs/$org/account/mcp", icon: Bot, group: "account" }`.
- Make it findable by typing either "MCP" or "AI" — e.g. include "MCP" in the
  description key so cmdk's filter matches both terms.
- Add/verify the `nav` i18n keys in all four locales
  (`web/dash0/src/locales/{en,fr,es,de}/nav.json`).

### Follow-ups / cautions

- Update `web/dash0/e2e/mcp-page.spec.ts` to navigate to the new path, and
  add an assertion that the old `/orgs/$org/mcp` URL redirects.
- The string `/api/v1/mcp` (the MCP **API endpoint**, shown as copyable code
  on the page and in `design-reference.tsx:1468`) is unrelated to the page
  route and must not change.
- Coordinate with the in-flight MCP specs touching the same page:
  `specs/todos/2026-07-10-06-mcp-endpoint-web-ui-fallthrough.md` and
  `specs/todos/2026-07-06-01-mcp-one-click-connector-setup.md` (the latter's
  install deep-link, if it hardcodes the page path, needs the new URL).

## Implementation Plan

Frontend-only change (dash0). Both referenced coordination specs are already
merged (`specs/done/2026/07/`); the one-click connector deep-links target the
MCP **API endpoint** (`/api/v1/mcp`), not the page route, so no coordination
edit is needed there. The backend `GET /api/v1/mcp` browser redirect still
targets the org-less `/dash0/mcp` landing route (unchanged); that route is
updated to forward straight to the new page path.

1. **Move the page under Account.** `git mv routes/orgs/$org/mcp.tsx
   routes/orgs/$org/account.mcp.tsx`; change its `createFileRoute` id from
   `/orgs/$org/mcp` to `/orgs/$org/account/mcp`. The page now renders inside
   the Account layout (title + tabs + Outlet); keep the page's own
   `ai.title` heading (the e2e asserts an "AI assistants" heading) and body.
2. **Redirect the old path.** New `routes/orgs/$org/mcp.tsx` becomes a
   redirect route (`beforeLoad` → `throw redirect` to
   `/orgs/$org/account/mcp`), preserving the `?from=get` search param via
   `validateSearch`, so bookmarks/external links don't 404.
3. **Avoid redirect chains.** Point the org-less `routes/mcp.tsx` `Navigate`
   and `organization.ai.tsx` redirect straight at `/orgs/$org/account/mcp`.
4. **Account layout tab.** Add `{ label: t("nav:ai"), path:
   "/orgs/$org/account/mcp" }` to `account.tsx`'s `tabs`, after Tokens.
5. **Breadcrumbs.** In `$org.tsx`, drop the dead top-level `isMcp` branch (and
   its now-unused `Bot` import); add an `isAi` sub-label to the existing
   Account breadcrumb branch so it reads "Account › AI assistants".
6. **Sidebar.** Remove the top-level "AI" `navItems` entry from
   `AppSidebar.tsx` (now reachable via Account tabs + palette) and its unused
   `Bot` import.
7. **Command palette.** Add `{ titleKey: "ai", descriptionKey:
   "command.aiDescription", path: "/orgs/$org/account/mcp", icon: Bot, group:
   "account" }` to `CommandMenu.tsx`'s `pages` (import `Bot`). The description
   key includes "MCP" so cmdk's filter matches both "AI" and "MCP".
8. **i18n.** `nav:ai` already exists in all four locales; add
   `command.aiDescription` (with "MCP") to `{en,fr,es,de}/nav.json`.
9. **E2E.** Update `e2e/mcp-page.spec.ts` to navigate to
   `orgs/test/account/mcp`, fix the redirect-target assertions, and add a test
   that the old `/orgs/test/mcp` redirects to the new path. Extend
   `e2e/command-menu.spec.ts` to assert the AI/MCP entry is present and
   findable by typing "MCP".
10. **QA.** `make build-dash0` (regenerates `routeTree.gen.ts`) + `cd
    web/dash0 && bun run lint`; author the e2e updates.
