---
model: sonnet
effort: medium
---

# Checks list: groups with 0 checks show an endless skeleton instead of an empty state

## Problem

On `/dash0/orgs/$org/checks` (observed on https://solidping.k8xp.com), a group
that contains **zero checks** (e.g. a freshly created "Cron" group, badge
showing `0`) renders three skeleton placeholder rows instead of anything that
says the group is empty. The skeletons can stay up indefinitely.

Root cause, in [checks.index.tsx](web/dash0/src/routes/orgs/$org/checks.index.tsx):

- Each group section decides between rows → skeletons → "No checks" using an
  `isLoading` prop (`checks.index.tsx:784-807`): an empty bucket shows
  skeletons while `isLoading` is true and only shows the truthful `noChecks`
  empty state once the stream is drained.
- That prop is fed `checksStreaming = checksLoading || hasNextPage ||
  isFetchingNextPage` (`checks.index.tsx:1020`).
- The checks list is an **infinite-scroll** query with one page-level sentinel
  (`checks.index.tsx:1512`). In an org with more checks than one page,
  `hasNextPage` stays `true` until the user scrolls far enough to pull every
  page. So an empty group's bucket "reads as loading" for as long as the user
  doesn't fully scroll the list — i.e. effectively forever on orgs with many
  checks.

The irony is the header badge already knows the truth: it renders
`group.checkCount` (server-side count, independent of loaded pages — see
comment at `checks.index.tsx:1025-1027`), and shows `0` right above the fake
skeletons.

## Proposal

Short-circuit the loading heuristic with the authoritative count: when **not
filtering** and `group.checkCount === 0`, render the empty state immediately —
never skeletons — since no page of the stream can ever deliver a row for that
group.

Concretely, in `CheckGroupSection` (`checks.index.tsx:776-812`):

- Compute an effective loading flag, e.g.
  `const showAsLoading = isLoading && (isFiltering || group.checkCount !== 0);`
  and use it in place of `isLoading` in the rows/skeleton/empty-state ternary.
- Keep the current behavior while filtering (search/status/labels/internal):
  `checkCount` is the unfiltered total, so a filtered-to-zero bucket must keep
  deferring to the stream (`checks.index.tsx:1468-1476` already hides
  zero-match groups once the stream resolves).
- Rendering of the empty state itself: reuse the existing `noChecks` branch
  (`checks.index.tsx:803-807`). Optionally make it slightly more helpful for
  the unfiltered-empty-group case (e.g. "This group is empty" plus a link to
  `/orgs/$org/checks/new?group=<uid>`), but that's a nice-to-have — the author
  has no strong preference on the rendering; the bug is the skeletons.

Notes / edge cases:

- The ungrouped section has the same skeleton logic
  (`checks.index.tsx:861-867`) but no server-side count to consult, and it
  already hides itself entirely when resolved-empty (`checks.index.tsx:843-845`)
  — leave it as is.
- `group.checkCount` refreshes with the groups query; a check just moved out
  of a group could leave a brief stale non-zero count. That only means the old
  (skeleton) behavior for one poll cycle — acceptable.
- Add/extend a Playwright e2e (`web/dash0/e2e/`): create a group with no
  checks in an org that also has enough checks to paginate isn't practical in
  e2e; it's sufficient to assert that a checks page with an empty group shows
  the empty-state text (not skeletons) once loaded, and that skeletons don't
  persist after the list settles.
