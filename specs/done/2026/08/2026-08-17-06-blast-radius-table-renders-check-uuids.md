---
model: sonnet
effort: medium
---

# The blast-radius table renders raw check UUIDs and overflows on mobile

## Problem

On an incident detail page with rolled-up children, the **Blast radius** card
lists every affected check by its **raw UUID** instead of its name. Observed on
a real incident with 6 affected checks (org `acme`), every row read
`84698cfb-5b01-4fab-b898-13beda200722` rather than the check's name.

On a phone this compounds into a broken layout:

- A 36-character UUID has no natural break points, so each cell wraps to **four
  lines**, making every row ~180 px tall.
- That forces the table wider than the viewport. The `Table` primitive does wrap
  its content in `overflow-auto` ([table.tsx:8](web/dash0/src/components/ui/table.tsx:8)),
  so it scrolls rather than truly clipping — but the third column's header and
  its badge are both cut mid-word off the right edge, so the card reads as
  broken on first paint.

### Root cause — a missing `with=check`, not a backend gap

`BlastRadiusCard` fetches children through `useIncidents`
([incidents.$incidentUid.tsx:1252-1257](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:1252))
and renders

```tsx
{child.checkName || child.checkSlug || child.checkUid}
```

([incidents.$incidentUid.tsx:1282](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:1282)).

The fallback chain is correct; it lands on the UUID because the first two are
`null`. The list endpoint hydrates check details **only when asked** — the
service gates it on `opts.WithCheck`
([service.go:1950](server/internal/handlers/incidents/service.go:1950)):

```go
checkMap := s.buildCheckMap(ctx, org.UID, incidents, opts.WithCheck)
```

and this call site never passes `with`. Verified against the live API — same
incident, same children:

| Request | `checkName` |
|---|---|
| `?causedByIncidentUid=…&size=50` (what the card sends today) | `null` |
| `?causedByIncidentUid=…&size=50&with=check` | `"api.acme-staging.io/datalake (http)"` |

So this is **a one-parameter frontend fix, not a backend change**. The
single-incident detail endpoint already returns `checkName` unconditionally,
which is why the page header shows a proper name while the table below it shows
UUIDs — the inconsistency that makes the bug look like missing data.

### Two smaller defects in the same table

1. **`detail.state` is missing from every locale.** The header renders
   `t("detail.state", { defaultValue: "State" })`
   ([incidents.$incidentUid.tsx:1273](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:1273)),
   and the key is absent from `en`, `fr`, `de` **and** `es`, so all four
   languages silently display hardcoded English.
2. **A badge string is reused as a column header.** The third column's header is
   `t("rollup.rolledUpBadge")` → `"rolled up"`, the same string as the badge in
   the cells below it ([incidents.$incidentUid.tsx:1274](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:1274)).
   A header labelled identically to its own cell contents carries no
   information, and it is the column being clipped on mobile.

## Proposal

### 1. Ask for the check details

Add `with: "check"` to the `useIncidents` options in `BlastRadiusCard`. This
alone replaces every UUID with a real name and removes most of the layout
damage, since names break at word boundaries where UUIDs cannot.

Keep the existing `checkName || checkSlug || checkUid` fallback — a check that
has since been hard-deleted has no row to hydrate from, and the UUID remains the
correct last resort. Do **not** replace it with a blank or a dash.

### 2. Make the cell resilient to long names

Names are not automatically short — the real data includes
`"api.acme-staging.io document-storage version (http)"` (51 chars). The check
cell should stay readable without dominating the row: constrain it and truncate
with an accessible full value (`title`, or the tooltip primitive the design
reference already ships) rather than letting it wrap freely.

Check the live design reference at
`/dash0/orgs/default/design-reference` first — if a truncating table-cell
pattern already exists there, use it rather than hand-rolling one; if it does
not, add it there as part of this change so the catalog stays canonical.

### 3. Make the rows navigable — to the child incident *and* to the check

Today the only link in a row is the icon-only fourth column
([incidents.$incidentUid.tsx:1294-1304](web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:1294)),
a ~14 px `ExternalLink` pointing at the child incident. The name cell itself is
inert text. Two changes:

- **Check name → child incident.** Wrap the name cell's content in a `Link` to
  `/orgs/$org/incidents/$incidentUid` with `child.uid` — the same target as
  today's icon column. The name is the row's natural, large click target; the
  tiny icon can then be dropped or kept as a secondary affordance, whichever
  reads better against the design reference.
- **A way to reach the check itself.** Add a link to
  `/orgs/$org/checks/$checkUid` using `child.checkUid` — most likely as a
  dedicated icon column (or an icon beside the state badge if four+ columns
  don't fit on mobile, see §5). Guard it on `checkUid` being present: a
  hard-deleted check has no page to link to, and the row must still render.

Two different link targets in one row must be visually distinguishable — check
other tables and the design reference for an existing two-destination row
pattern before inventing one, and keep touch targets big enough for mobile.

### 4. Fix the header row

- Add `detail.state` to all four locale files (`en`, `fr`, `de`, `es`) and drop
  the `defaultValue` fallback.
- Give the rolled-up column a header that describes the column rather than
  repeating the badge — e.g. a `rollup.pagingColumn` key ("Paging"), with the
  cell keeping the existing `rolled up` badge. Add it to all four locales.
- If a header-less link column remains after §3, make sure it is not what gets
  pushed off-screen when space is tight; if the check-link column gets a header,
  add its key to all four locales like the others.

### 5. Verify it on a narrow viewport

The repo rule is that every page is fully usable on mobile. With names in place,
confirm at 375 px that all four columns are reachable and no header is clipped
mid-word. If the four columns still cannot coexist at that width, prefer
prioritising **check name + state + link** and folding the paging indicator into
the row (e.g. an icon next to the state badge) over a horizontal scroll the user
has to discover.

## Verification

- A blast-radius table for an incident with rolled-up children shows check
  **names**, and shows a UUID only when the underlying check no longer exists.
- Clicking a check's name navigates to that child incident's detail page, and
  each row also offers a link to the check's own page
  (`/orgs/$org/checks/$checkUid`); a row whose check was hard-deleted still
  renders, with no dead check link.
- Playwright E2E at a 375 px viewport: no column header is visually truncated,
  and the child-incident link is reachable without horizontal scrolling.
- `detail.state` and the new paging-column key resolve in all four locales; no
  `defaultValue` fallback remains in this table.
- The query key change from adding `with` does not collide with other
  `useIncidents` callers' caches (`with` is already part of `queryOptions` and
  therefore of the key — confirm no caller relies on sharing that entry).
