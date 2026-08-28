---
model: sonnet
effort: high
---

# Checks and incidents list tables are unusable on mobile — hide secondary columns responsively

## Problem

CLAUDE.md mandates that "all pages must be fully usable on mobile", and the repo
already has an established idiom for wide tables: hide secondary columns with
`hidden <bp>:table-cell` applied to the `TableHead` **and** its matching
`TableCell` (see `web/dash0/src/routes/orgs/$org/checks.scheduling.tsx:100` and
`web/dash0/src/routes/orgs/$org/organization.audit.tsx:446`).

The two most-visited list pages never got that treatment:

- **Checks** (`web/dash0/src/routes/orgs/$org/checks.index.tsx`) renders 6
  columns — name / type / target / status / response / actions (header at
  `checks.index.tsx:626-635`) — with no responsive classes. On a phone the
  whole table falls back to the `overflow-x-auto` wrapper and side-scrolls.
  The status column is even redundant at every width: the `StatusDot` inside
  the name cell (`checks.index.tsx:489`) already conveys up/down/disabled.
- **Incidents** (`web/dash0/src/routes/orgs/$org/incidents.index.tsx`) renders
  6 columns — incident / check / state-dot / started / duration / failures
  (header at `incidents.index.tsx:291-300`). Extra problems:
  - The check column is frequently a literal duplicate of the first column,
    because the incident title falls back to the check name
    (`incidents.index.tsx:327-329`).
  - The state dot sits in its own dedicated `w-10` mid-table column
    (`incidents.index.tsx:295`, cell at `:393-408`) instead of living in the
    first cell like the checks table does — wasted width at every breakpoint.
  - The badge cluster in the first cell (snoozed / acked / relapse / flapping /
    rolled-up, container at `incidents.index.tsx:313`) is `flex items-center
    gap-2` without `flex-wrap`, so a badged incident forces horizontal
    overflow on narrow screens regardless of column hiding.

These are the pages an operator opens from an alert on their phone — they
should degrade to a tight "name + status + one live signal" view, not a
side-scrolling desktop table.

## Proposal

Apply the existing `hidden <bp>:table-cell` idiom (always on the head/cell
*pair*), keeping identity, live signal, and actions visible at every width.

**Checks table** (`ChecksTable` / `CheckRow` in `checks.index.tsx`):

| Column | Head / cell | Behavior |
|---|---|---|
| Name (with StatusDot) | `:628` / `:481` | always visible |
| Type | `:629` / `:513` | `hidden sm:table-cell` |
| Target | `:630` / `:523` | `hidden md:table-cell` |
| Status badge | `:631` / `:526` | `hidden md:table-cell` (dot already carries it) |
| Response time | `:632` / `:529` | always visible — the one live numeric signal; the pill is ~60px |
| Actions | `:633` / `:546` | always visible |

**Incidents table** (`incidents.index.tsx`):

1. **Fold the state dot into the first cell** (next to `#number` / title),
   mirroring the checks pattern, and drop the dedicated `w-10` column. Update
   the `GroupHeaderRow` `colSpan={6}` (`incidents.index.tsx:130`) to match the
   new column count.
2. Column tiers on the remaining columns:
   - Incident (title + badges + now the dot): always visible.
   - Check (`:294` / `:379`): `hidden md:table-cell`.
   - Started (`:296` / `:409`): always visible — for resolved incidents "2h
     ago" scans better than a duration.
   - Duration (`:297` / `:419`): `hidden sm:table-cell` — for active incidents
     it merely restates "now − started".
   - Failures (`:298` / `:422`): `hidden md:table-cell`.
3. Add `flex-wrap` to the badge container at `incidents.index.tsx:313` so
   badge-heavy incidents wrap instead of overflowing.

**Design reference**: add a "responsive table" entry to
`web/dash0/src/routes/orgs/$org/design-reference.tsx` documenting the
head+cell `hidden <bp>:table-cell` pairing and the tiering principle
(identity + primary status + actions always visible; widest/most redundant
columns go first), so the convention is canonical for follow-up pages.

**QA**

- Playwright checks at a 375px viewport (`page.setViewportSize`) asserting the
  checks and incidents list pages produce no horizontal document scroll
  (`document.documentElement.scrollWidth <= clientWidth`) with seeded data,
  and that name, response time (checks), started (incidents) and row actions
  remain visible.
- Existing desktop-viewport E2E suites must stay green — note several
  selectors target cells being hidden on mobile only (`incident-started-at`,
  `incident-number`, `change-group-action`); at the default desktop viewport
  nothing changes.
- No new dash0 ESLint errors (the base already carries pre-existing
  react-hooks debt — scope lint to no *new* findings).

## Implementation Plan

1. **Checks table** (`checks.index.tsx`):
   - Head row: `type` → `hidden sm:table-cell`, `target` and `status` →
     `hidden md:table-cell`. Name and response-time heads stay visible; the
     trailing `w-[50px]` actions head is untouched.
   - Matching body cells get the same classes. The name cell additionally
     gets `max-w-0` on the `TableCell` (mirrors the "Truncated cell" pattern
     already in the design reference) plus `min-w-0`/`truncate` on the inner
     flex row and name text, so a long check name can't force the page wider
     once the row is down to 3 visible columns on a phone.

2. **Incidents table** (`incidents.index.tsx`):
   - Fold the state dot into the first cell, next to the `#number` badge,
     and drop the dedicated `w-10` head/cell — 6 columns become 5.
   - Update `GroupHeaderRow`'s `colSpan={6}` to `colSpan={5}` (confirmed via
     grep this is the only `colSpan` in the file).
   - Head tiers: incident always visible, check → `hidden md:table-cell`,
     started always visible, duration → `hidden sm:table-cell`, failures →
     `hidden md:table-cell`. Matching body cells get the same classes.
   - Add `flex-wrap` to the badge container in the first cell (`flex
     items-center gap-2` → `flex items-center gap-2 flex-wrap`).
   - Give the first cell `max-w-0` and constrain the title `Link` with
     `truncate` (title is the longest unbounded string in that cell) so it
     can't force horizontal scroll at 375px once the dot/number/badges share
     the cell.

3. **Design reference** (`design-reference.tsx`): add a "Responsive table"
   subsection inside `DataDisplaySection` (after "Truncated cell"), with its
   own nav entry (`responsive-table` — actually appended as a subsection of
   the existing `data-display` entry, matching how "Clickable rows" and
   "Truncated cell" are subsections rather than separate nav ids) showing a
   small example table with `hidden sm:table-cell` / `hidden md:table-cell`
   columns, plus a code snippet and prose describing: (a) always apply the
   class to the `TableHead` AND every matching `TableCell` in that column —
   a mismatched pair silently shifts every following column at the
   breakpoint where it applies; (b) tiering principle — identity + the one
   live signal + row actions stay visible at every width, the widest or most
   redundant columns fold away first; (c) `hidden <bp>:table-cell` alone
   doesn't stop overflow — a cell whose content can't shrink (a long name, a
   UUID) still forces the page wider, so pair it with `max-w-0`/`truncate`
   on that column, verified against `document.documentElement.scrollWidth
   <= clientWidth` at 375px.

4. **Tests**:
   - New Playwright spec (or additions to existing check/incident E2E specs)
     asserting, at a 375px viewport: no horizontal document scroll on both
     the checks and incidents list pages with seeded data, and that name,
     response time (checks), started-at (incidents), and row actions remain
     visible/interactable.
   - Confirm existing desktop-viewport selectors (`incident-started-at`,
     `incident-number`, `change-group-action`) are not hidden at the default
     desktop viewport — only at `sm`/`md` breakpoints below it, so existing
     suites stay green untouched.
   - Local dev server isn't in `SP_RUNMODE=test`, so the new E2E spec is
     authored but not run locally; report that explicitly.

5. **Gate**: `npx tsc --noEmit`, targeted `eslint`, `vitest run` iteratively;
   then one full `make build-dash0` + `bun run lint` pass before the
   "all checks passing" commit.
