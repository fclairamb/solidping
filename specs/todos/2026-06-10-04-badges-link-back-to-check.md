# Badges page: link back to the selected check

## Context
The badges route (`web/dash0/src/routes/orgs/$org/badges.tsx`,
e.g. `?check=snmp-sysdescr&components=status,availability,duration`) lets the
operator generate status/availability/duration badges for a check. The check is
chosen via a `Select` and stored in the `check` search param (slug when
available, otherwise uid); the component resolves it to `selectedCheck` by
uid-then-slug. The page title is the generic "Badges" — there is **no way to
navigate from here back to the check** the badges are for. The check name only
appears inside the dropdown and the preview `alt` text.

Every other page links checks via the canonical route
`to="/orgs/$org/checks/$checkUid"` (used by the incident detail page, events,
status pages, etc.). The badges page should offer the same affordance.

## Goal
When a check is selected, show a link back to that check's detail page near the
page title, so the operator can jump straight to the check they're badging.

## Behaviour
- When `selectedCheck` is defined, render a link using the canonical route:
  ```tsx
  <Link
    to="/orgs/$org/checks/$checkUid"
    params={{ org, checkUid: selectedCheck.uid }}
    search={{ graphPeriod: undefined, graphFull: undefined }}
    className="text-sm text-primary hover:underline inline-flex items-center gap-1"
  >
    <ArrowLeft className="h-4 w-4" />
    {selectedCheck.name || selectedCheck.slug || selectedCheck.uid.slice(0, 8)}
  </Link>
  ```
  (Match the `search` shape other check links already pass so the route's search
  validation stays happy; verify against an existing check `<Link>` in the
  codebase.)
- Placement: in/under the page header next to the "Badges" title, so it reads as
  "← {check name} / Badges". Keep it on its own line above the title on mobile.
- Hidden entirely when no check is selected (no `selectedCheck`).
- Pass `selectedCheck.uid` (not the slug stored in the search param) to
  `params.checkUid`, since the route keys on uid.

## Out of scope
- No change to badge generation, the components/period/style controls, the
  preview, or the `check` search-param storage format.
- No change to the check detail page.

## Testing
dash0 Playwright E2E (`web/dash0/e2e/`); badges coverage in `e2e/badges.spec.ts`
(create if absent).
- With `?check=<slug>`: the back-to-check link is visible and shows the check
  name; clicking it navigates to `/dash0/orgs/$org/checks/<uid>`.
- With no `check` param: the link is absent.
- Manual: `make dev-test`, open the badges page with a check selected, confirm
  the link on desktop + mobile, light + dark.

## Implementation Plan
1. Import `ArrowLeft` (lucide-react) and `Link` (if not already) in
   `badges.tsx`.
2. Render the conditional back-to-check link in the header region, guarded on
   `selectedCheck`, using the canonical route + `selectedCheck.uid`.
3. Confirm the `search` prop matches existing check links so search validation
   passes.
4. Add/extend `e2e/badges.spec.ts` per Testing.
5. Verify: `bun run lint` (dash0), `make test-dash`, manual mobile + dark check.
