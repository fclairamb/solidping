---
model: sonnet
effort: medium
---

# Private locations "Last seen" should show a relative time in local time

## Problem

On the private locations page
(`/dash0/orgs/$org/organization/private-locations`), the agents table renders
the "Last seen" column as a raw absolute timestamp:

- `web/dash0/src/routes/orgs/$org/organization.private-locations.index.tsx:420-423`
  renders `new Date(agent.lastSeenAt).toLocaleString()` (or "never").

An absolute timestamp is hard to scan for the question the column actually
answers — "is this agent alive right now?". The user has to mentally diff the
timestamp against the current time. It should read as a relative time ("5s
ago", "2 minutes ago", …) that updates live, with the exact local-time
timestamp still available (e.g. as a tooltip).

## Proposal

1. **Reuse the existing pattern instead of inventing a new one.**
   `web/dash0/src/components/checks/check-summary-cards.tsx:25-51` already has
   `LiveDuration` / `LiveDurationAgo`: a 1s-interval live counter plus a
   `formatDuration` helper, wrapped in the locale-appropriate "ago" template
   via the `detail.summary.ago` translation key ("5m ago" / "il y a 5m" /
   "vor 5m"). Extract this into a shared component (e.g.
   `web/dash0/src/components/shared/relative-time.tsx` or a UI primitive) and
   have `check-summary-cards.tsx` consume the shared version so there is a
   single implementation.

2. **Use it in the private-locations agents table.** Replace the
   `toLocaleString()` cell with the relative-time component:
   - Primary text: relative time ("5s ago", "2m ago", "3h 12m ago", …),
     ticking live (the page already polls agents; a 1s interval matches the
     existing `LiveDuration` behavior).
   - Keep the exact timestamp accessible: set `title=` (or a tooltip) to the
     full local-time string (`toLocaleString()`), so hovering shows the
     precise moment.
   - Keep the existing "never" fallback for agents with no `lastSeenAt`.

3. **i18n**: reuse/mirror the existing "ago" translation template so FR/DE/ES
   render correctly (don't hard-code the "ago" suffix). Check the
   `privateLocations` namespace for where the key should live if the shared
   component takes the translated template as a prop.

4. **Design reference**: per repo convention, if this becomes a reusable
   primitive, add it to
   `web/dash0/src/routes/orgs/$org/design-reference.tsx` so the catalog stays
   canonical.

5. **Tests**: adjust/extend the Playwright coverage for the private locations
   page (`web/dash0/e2e/`) if it asserts on the "Last seen" cell format; a
   cell matching `/ago|never/` (or the translated equivalent) is enough.

## Notes / open questions

- Other pages showing raw `toLocaleString()` timestamps could adopt the same
  component later; this spec only requires the private-locations agents table
  (plus the extraction refactor in `check-summary-cards.tsx`).
- For very old "last seen" values (days+), `formatDuration` currently tops out
  at hours (`Xh Ym`). Extending it with a days unit (`3d 4h ago`) is in scope
  if trivial; otherwise note it and keep hours.
