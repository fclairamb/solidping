---
model: sonnet
effort: low
---

# The check page "Recent Incidents" card lists every incident and shows no incident ID

## Problem
On the check detail page, the "Recent Incidents" card renders every row of the incidents query, which fetches up to 100 (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:940-944`, `size: 100`, rendered at `:2044-2110`). A check with a long history gets a very long card. Rows also show no incident ID (only Started / State / Duration), while the org incidents list shows `#{incident.number}` as a link (`web/dash0/src/routes/orgs/$org/incidents.index.tsx:485-492`, `data-testid="incident-number"`).

The same 100-row query also feeds the degraded chart bands (`:950-964`) and the Incidents stat card total (`:1458`), so the fetch size must stay as is.

## Proposal
1. In `checks.$checkUid.index.tsx`, render only the 10 most recent incidents in the card: `incidents.data.slice(0, 10)` at `:2062`. Sort by `startedAt` descending before slicing so "last 10" does not depend on backend order. Do not change the `useIncidents` call at `:940`.
2. Add a first column "ID" (new key `checks:detail.incidents.id`, in `web/dash0/src/locales/{en,fr,de,es}/checks.json` next to the existing `detail.incidents.*` keys) showing `#{incident.number}`, using the same rendering as `incidents.index.tsx:485-492` (a link to `/orgs/$org/incidents/$incidentUid`, `data-testid="incident-number"`). Fall back to `-` when `number` is missing. Stop propagation on the link click so the row `onClick` doesn't navigate twice.
3. When the check has more than 10 incidents, add a "View all" link under the table to `/orgs/$org/incidents` with `checkUid` and `state=all` search params, the same target the Incidents stat card uses (see `web/dash0/e2e/check-incidents-card-click-through.spec.ts`). Reuse that card's link construction.
4. Update the description text if needed so it doesn't imply the full list (e.g. "Last 10 issues related to this check", all 4 locales).

## Tests
- `web/dash0/e2e/check-recent-incidents-limit.spec.ts` (new), with `page.route` mocking `/incidents` for a check:
  - 15 incidents returned: exactly 10 `incident-row-*` rows, they are the 10 with the latest `startedAt` (even if the mock returns them unsorted), and the "View all" link is present with `checkUid`.
  - 3 incidents returned: 3 rows, no "View all" link (negative case).
  - each row shows `#<number>` and clicking the number lands on `/incidents/<uid>`.
  - 0 incidents: the card is still hidden (existing behaviour).
- Existing `check-incidents-slug.spec.ts` and `check-incidents-card-click-through.spec.ts` keep passing.
- `bun run test:unit` (locale key parity across en/fr/de/es).

## To verify
- Whether `IncidentSummary` in the generated client exposes `number` on this list endpoint (it does on the org list, so probably yes).
- The backend's default `/incidents` ordering, in case the client-side sort is redundant.
