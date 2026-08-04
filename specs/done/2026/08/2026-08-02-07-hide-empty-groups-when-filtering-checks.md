---
model: sonnet
effort: medium
---

# Hide empty check groups when a search/filter is active (GitHub issue #171)

## Problem

[Issue #171](https://github.com/fclairamb/solidping/issues/171): searching the checks
list (e.g. filtering on the "down" status) still renders every group header, most of
them empty — the UI looks broken.

Why it happens: the filtered check rows and the group sections come from two
unrelated queries that are never reconciled.

- Filtering is server-side: `q`, `status`, `labels`, `internal` are passed to
  `GET /checks` (`web/dash0/src/routes/orgs/$org/checks.index.tsx:930-941`).
- Group sections come from `useCheckGroups(org)` (`checks.index.tsx:898-905`,
  hook at `web/dash0/src/api/hooks.ts:632-643`), which has **no filter params**.
- The render loop `{(groups || []).map(...)}` at `checks.index.tsx:1395` renders
  every group unconditionally; `CheckGroupSection`'s empty branch shows a
  "no checks" placeholder (`checks.index.tsx:784-786`) instead of hiding.
- The ungrouped section already self-hides, but its guard at `checks.index.tsx:821`
  only checks `!search` — it misbehaves under a `status` or `labels` filter too.
- Group headers also show full-fleet counts (`group.checkCount` at :691,
  `group.memberStatusCounts` at :642) even when the visible rows are filtered.

## Proposal

Client-side fix — no backend change needed:

1. Introduce a single `isFiltering` boolean derived from all active filters
   (`debouncedSearch`, `statusParam`, `labelsParam`, and a non-default
   `internalFilter`).
2. In the groups render loop (`checks.index.tsx:1395`), when `isFiltering` is true,
   skip groups whose bucket `checksByGroup.get(group.uid)` is empty. When not
   filtering, keep today's behavior (empty groups stay visible with their
   placeholder, so users can find/manage them).
3. Fix the `UngroupedChecksSection` guard (:821) to use the same `isFiltering`
   predicate instead of `!search` only: hide when filtering yields zero ungrouped
   rows; keep the current empty-state otherwise.
4. While filtering, prefer showing the count of *matching* checks on the group
   header (bucket length) rather than the unfiltered `group.checkCount` /
   `memberStatusCounts` badges, so headers don't contradict their contents.
   (If this bloats the change, it can be split out — hiding empty groups is the
   core of the issue.)
5. Apply the same treatment to the by-host view buckets (`checks.index.tsx:1003-1020`)
   if hosts can end up empty under filters.
6. **Tests**: extend the dash0 Playwright checks-list spec — with a group of only-up
   checks and a down check elsewhere, filtering `status=down` shows only the group
   containing the down check; clearing the filter brings all groups back.

Don't wait for pending results: only hide once the filtered query has resolved
(`isLoading` false), to avoid group sections flickering out during load.
