---
model: sonnet
effort: medium
---

# Add small, contextual documentation links across the dashboard

## Problem

The docs site is embedded in the binary and served same-origin at `/docs` on every
host, but the dashboard barely points at it. Today only two pages link to the
docs, both hand-rolled:

- `web/dash0/src/routes/orgs/$org/account.mcp.tsx:267` → `/docs/features/mcp`
- `web/dash0/src/routes/orgs/$org/organization.private-locations.register.tsx:664` → `/docs/features/private-locations`

Users on any other page (checks, incidents, on-call, status pages, maintenance
windows, integrations…) have no discoverable path to the relevant documentation,
even though a matching docs page usually exists under
`web/docs/docs/features/` or `web/docs/docs/configuration/`.

We want documentation links **everywhere in the app**, but **small and
non-intrusive** — no banners, no callouts; a subtle affordance a user can find
when they need it and ignore otherwise.

## Proposal

1. **Add a `DocsLink` primitive** (e.g. `web/dash0/src/components/shared/docs-link.tsx`):
   a small ghost icon button (`BookOpen` from lucide, muted color, ~h-8 w-8)
   wrapping an `<a href="/docs/..." target="_blank" rel="noopener">` with a
   tooltip ("Documentation") and an `aria-label`. Links are same-origin relative
   paths (`/docs/features/...`), so they work on every host with no config.
   Add it to the design reference page
   (`web/dash0/src/routes/orgs/$org/design-reference.tsx`) with its import line,
   per the repo convention that new primitives land in the catalog.

2. **Extend `PageHeader`** (`web/dash0/src/components/shared/page-header.tsx`)
   with an optional `docsHref?: string` prop that renders the `DocsLink` in the
   header (next to `actions`, or right after the title). This gives every page a
   uniform, discreet placement with a one-line change per route.

3. **Wire it across the org routes**, mapping each area to its best existing
   docs page — only where a genuinely relevant page exists, never a forced link
   to `/docs/intro`. Suggested mapping (verify each target renders):

   | Dashboard area | Docs path |
   |---|---|
   | Checks (list/new/edit/detail) | `/docs/features/check-types` |
   | Check groups | `/docs/features/check-groups` |
   | Incidents | `/docs/features/incidents` |
   | Escalation policies / on-call | `/docs/features/on-call` |
   | Status pages | `/docs/features/status-pages` |
   | Status page custom domain settings | `/docs/features/custom-domains` |
   | Maintenance windows | `/docs/features/maintenance-windows` |
   | Private locations | `/docs/features/private-locations` |
   | Integrations / notification destinations | `/docs/configuration/notifications` |
   | SSH tunnels | `/docs/features/ssh-tunnels` |
   | Account → MCP | `/docs/features/mcp` (replace the ad-hoc link) |
   | Account → API tokens | `/docs/api` |
   | Organization → security/auth settings | `/docs/configuration/authentication` |

   Pages with no matching docs (e.g. dependencies, discovery, badges — unless a
   docs page exists) simply don't pass `docsHref`. Convert the two existing
   hand-rolled links to the shared component so the styling is consistent.

4. **Tests**: extend an existing Playwright spec (or add a small one in
   `web/dash0/e2e/`) asserting that a couple of representative pages (e.g.
   checks list, status pages list) render the docs link with the expected
   `href`, and that a page without a mapping renders none.

## Decisions / open questions

- Scope is **dash0 only**; status0 (public status page) and the marketing site
  are out of scope.
- Header-level links only for now. Field-level contextual links inside forms
  (e.g. next to a specific check option) can come later once the primitive
  exists — don't sprinkle them in this pass.
- Mobile: the icon button must stay a comfortable touch target and must not
  wrap the header awkwardly on small screens (all pages must remain fully
  usable on mobile, per repo conventions).
