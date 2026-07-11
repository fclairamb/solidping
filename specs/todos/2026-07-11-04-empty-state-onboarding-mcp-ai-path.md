# Empty-state onboarding doesn't offer the MCP / AI path for creating checks

## Problem

When an org has no checks yet, the dashboard
(`https://solidping.k8xp.com/dash0/orgs/default`) shows the
`EmptyStateOnboarding` hero
(`web/dash0/src/components/dashboard/empty-state-onboarding.tsx`, rendered
from `web/dash0/src/components/dashboard/dashboard-page.tsx:360` when
`isEmptyOrg` — `dashboard-page.tsx:280`). It offers exactly two paths:

1. Quick-create a single check via the HTTP / Ping / SSL chips and a one-field
   form (`empty-state-onboarding.tsx:102-167`).
2. "Need more control? Open the full check editor" linking to
   `/orgs/$org/checks/new` (`empty-state-onboarding.tsx:169-177`).

Meanwhile SolidPing ships a full MCP server (`server/internal/mcp/`, ~40
tools including `create_check`, `validate_check`, check-type discovery,
integrations, status pages…) with a polished per-client setup page at
`/orgs/$org/account/mcp` (`web/dash0/src/routes/orgs/$org/account.mcp.tsx`):
one-click deep links for Cursor / VS Code, a `claude mcp add` command for
Claude Code, OAuth 2.1 + PAT auth, and docs at `/docs/features/mcp`.

For a user landing on an empty org, the AI route is arguably the *best*
onboarding story — "connect your AI assistant and ask it to set up all your
checks" — yet the empty state never mentions it. The MCP page is buried under
Account, somewhere a brand-new user has no reason to look
(`specs/done/2026/07/2026-07-10-10-mcp-page-command-palette-and-account-placement.md`
made it discoverable via the command palette, but not from onboarding).

## Proposal

Add a third onboarding path to `EmptyStateOnboarding`: propose connecting an
AI assistant through MCP to do the full check onboarding.

1. **UI**: below (or beside) the quick-create form, add a clearly separated
   secondary block — e.g. a bordered sub-card or a divider with "or" — along
   the lines of:

   > **Let AI set everything up** — Connect Claude, Cursor or VS Code to
   > SolidPing's MCP server and ask it to create and configure all your
   > checks for you. *[Set up MCP →]*

   The CTA links to the existing MCP setup page (`/orgs/$org/account/mcp`) —
   no need to duplicate the per-client cards inside the hero. Reuse
   design-reference primitives; keep the block usable on mobile.

2. **Keep the hierarchy right**: the quick-create form stays the primary
   action (it converts in one field); the MCP path is a prominent secondary
   option, not a replacement. Don't make the hero taller than one screen on
   mobile.

3. **Copy/i18n**: add the new strings under the `welcome.*` namespace in
   `web/dash0/src/locales/*/dashboard.json` alongside the existing keys, with
   translations for all supported locales.

4. **Tests**: extend the existing empty-state coverage (the component already
   carries `data-testid="quick-start-*"` hooks) with an assertion that the
   MCP link is present and points at the account MCP page; add a
   `data-testid` on the new CTA.

Open questions:

- Should the CTA deep-link even further — e.g. render the one-click
  Cursor/VS Code buttons or the `claude mcp add` command inline in the hero —
  or is a single link to the MCP page enough for v1? (Lean: single link for
  v1; the MCP page already does per-client setup well.)
- The MCP page lives under Account (`/orgs/$org/account/mcp`) but this is an
  org-level onboarding concern; fine to link across, just confirm the page
  renders correctly for a brand-new org with zero checks and no PAT yet.
- Should the empty state also mention the docs (`/docs/features/mcp`), or
  does the MCP page's existing docs link cover that?

## Implementation Plan

### Resolved open questions

1. **Deep-link depth**: single link to `/orgs/$org/account/mcp` for v1 — the
   MCP page already does per-client setup (Cursor/VS Code deep links,
   `claude mcp add`, generic config) well; duplicating those cards in the
   hero would bloat it past one mobile screen.
2. **Cross-section link (Account page from an org-level hero)**: fine. The
   MCP page derives everything from `window.location.origin` (no PAT, no
   checks required — see `account.mcp.tsx`), so it renders correctly for a
   brand-new empty org. The e2e test clicks through from the empty state
   with a stubbed-empty checks list to prove it.
3. **Docs link in the hero**: no — the MCP page already links the docs;
   keeping the hero to a single secondary CTA preserves the hierarchy.

### Steps

1. **UI** (`web/dash0/src/components/dashboard/empty-state-onboarding.tsx`):
   after the quick-create form, add an "or" divider (two hairlines flanking a
   muted "or") followed by a bordered sub-card (`rounded-md border bg-card`)
   containing a `Bot` icon, a short "Let AI set everything up" title +
   description, and an outline `Button asChild` wrapping a TanStack `Link`
   to `/orgs/$org/account/mcp` with `data-testid="quick-start-mcp-link"`.
   The quick-create form stays the primary action; the block is `max-w-md`
   like the form and stacks cleanly on mobile. The existing "Need more
   control?" full-editor hint stays, below the new block.
2. **i18n**: add `welcome.or` and `welcome.mcp.{title,description,cta}` to
   `web/dash0/src/locales/{en,fr,de,es}/dashboard.json`.
3. **Design reference**: the block is composed purely of existing
   design-reference primitives (Card border tokens, `Button asChild` +
   `Link`, lucide icon) — add a compact "secondary-path divider + sub-card"
   example to `design-reference.tsx` so the pattern stays canonical.
4. **Tests**: new `web/dash0/e2e/empty-state-onboarding.spec.ts` — stubs
   `GET /api/v1/orgs/test/checks*` to `{"data":[]}` (deterministic empty org
   without mutating shared state), asserts the existing `quick-start-*`
   hooks render, asserts `quick-start-mcp-link` is present with an href
   ending in `/orgs/test/account/mcp`, clicks it and asserts the MCP page
   heading renders, and re-checks visibility + no horizontal overflow at a
   375px mobile viewport.
5. **QA**: `make build-dash0`, `cd web/dash0 && bun run lint` (no new errors
   in touched files); run the new e2e file if a test-mode server is
   available, otherwise report it authored-but-not-run.
