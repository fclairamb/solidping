---
model: sonnet
effort: medium
---

# Filter the checks list on one check group (facet filter + click on a group header)

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/72

## Source

In-app report (server v0.48.0, checks list `/d/orgs/<org>/checks`). The reporter wants to narrow
the list to one check group, either by clicking the group or by picking it in the filter bar at
the top of the page. The report's last sentence says that, with this selected, the list cannot be
filtered another way: read as the current state, it says nothing in the page lets you isolate a group today
(right); read as a requirement, it would mean the group filter disables the other filters (see
Open questions).

## Problem

The checks index (`web/dash0/src/routes/orgs/$org/checks.index.tsx`) has no group dimension:

- `ChecksIndexSearch` / `validateSearch` (`:145`, `:201`) carry `labels`, `status`, `type`,
  `groupBy`, `q`, no group.
- The filter bar (`:1738`-`:1765`) offers status, type, labels and (super-admin) scope.
- The page's single `useInfiniteChecks` call (`:1246`) never passes `checkGroupUid`, though both
  the hook (`web/dash0/src/api/hooks.ts:840`) and the API
  (`server/internal/handlers/checks/handler.go:332`, `checkGroupUid=<uid>` or `none`) already
  support it.
- Group headers (`CheckGroupSection`, `:721`) only collapse/expand; the name (`:832`) is not a
  link or filter action.

So with 10+ groups the only way to focus on one is to collapse the others by hand. No backend
change is needed.

## Why a spec

- A UX decision the reporter didn't settle (interaction of the group filter with the others,
  what clicking a header does, since it already toggles collapse).
- Fix plus Playwright test likely over ~150 lines in a 2300-line route.

## Proposal

1. **URL param.** Add `group?: string` to `ChecksIndexSearch`: a group slug (validated with the
   existing `groupSlugRegex`, `:169`) or `none` for ungrouped checks. A slug keeps the URL
   readable and shareable; resolve it to the uid from `useCheckGroups(org)` before querying. An
   unknown slug shows an empty state with a "clear filter" action, never the whole list.
2. **Query.** Pass `checkGroupUid` (uid or `none`) to `useInfiniteChecks`. Include it in
   `isFiltering` and `hasActiveFacets`; `resetFilters` clears it.
3. **Facet.** A single-select "Group" filter in the filter bar, `testId="group-filter"`, listing the groups in `sortOrder` plus
   "Ungrouped". Hidden when the org has no groups.
4. **Header click.** In Groups mode, the group name in `CheckGroupSection` becomes a button
   (`data-testid="group-filter-link"`) that sets `?group=<slug>`; the chevron keeps toggling
   collapse. When the filter is active, the single shown section is expanded.
5. **Other filters stay usable** with the group filter (search, status, type, labels combine with
   it, as the API ANDs them), per the recommended answer below.
6. **Tests.** Playwright `web/dash0/e2e/checks-index-group-filter.spec.ts`: picking a group in the
   facet shows only its checks; clicking a header name does the same; status + group combine;
   reset clears it; `?group=none` shows only ungrouped checks; a positive control that other
   groups' checks are visible before filtering.

## Open questions

1. **Does the group filter combine with the other filters, or replace them?** Recommended:
   combine (AND), like every other facet on this page. The report's last sentence reads best as a description of today's gap; locking the other filters would be unlike the rest of
   the UI and remove a useful "down checks in group X" view. Trade-off: if the reporter really
   meant exclusivity, it is a small follow-up.
2. **Single or multiple groups?** Recommended: single, which the API supports as is. Multi-select
   needs a `checkGroupUid` list on the backend.

## Closing the issue

This spec closes fclairamb/solidping-business#72. The implementing PR body **must** carry a
`Closes fclairamb/solidping-business#72` line. The issue is already closed by `/fix-issues`; when
the spec is archived to `specs/done/`, verify it is still closed and, if not, close it:

    gh issue close 72 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"

## Resolved open questions

1. The group filter combines (AND) with the other filters. (autopilot: recommended answer)
2. Single group only. (autopilot: recommended answer)
