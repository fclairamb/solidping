---
model: opus
effort: high
---

# Checks index shows "No checks" for populated groups until the user scrolls to the bottom

## Problem

Reported by Cyrille on the acmetech org
(https://acmetech.slack.com/archives/C0BFC2XG4K0/p1784576213211839?thread_ts=1784573400.622229&cid=C0BFC2XG4K0):
on `/orgs/acmetech/checks`, group headers show a non-zero check-count badge
but the tables under them render the "No checks" empty state. Checks only
appear after scrolling to the very bottom of the page, repeatedly, 100 at a
time.

Root cause — four interacting pieces, introduced when spec 2026-07-17-02
replaced the per-group N+1 queries with a single page-level infinite query:

1. The page loads one flat stream of checks —
   `web/dash0/src/routes/orgs/$org/checks.index.tsx:585` uses
   `useInfiniteChecks` with `limit: 100` (the server caps limit at 100 anyway,
   `server/internal/handlers/checks/handler.go:163`) and buckets rows
   client-side by `checkGroupUid`.
2. The stream is ordered `created_at DESC, uid DESC`
   (`server/internal/db/postgres/postgres.go:1318`, mirrored in
   `server/internal/db/sqlite/sqlite.go`), which is unrelated to the group
   order the page displays. The first 100 checks land in essentially random
   buckets, so any group whose checks are all older than the newest 100
   renders empty while its badge shows the true `checkCount` (from the
   separate check-groups endpoint).
3. The empty state lies: `isLoading` on an infinite query is only true while
   the *first* page loads, so an unfilled group falls through to the
   `noChecks` empty state (`checks.index.tsx:470`) instead of a loading
   state. Same flaw in `UngroupedChecksSection` (`checks.index.tsx:507`).
4. The single IntersectionObserver sentinel sits below *all* sections
   (`checks.index.tsx:934`) with `threshold: 0.1` and no `rootMargin`, so
   with many groups it is far below the fold and only fires once the user
   physically reaches the bottom.

Any org with more than 100 checks hits this on first paint.

## Proposal

Two parts — a trust fix that ships in the frontend alone, and a pagination
ordering fix so the stream fills the page top-down.

### Part 1 — never render a false empty state, prefetch earlier (frontend)

- In `CheckGroupSection` and `UngroupedChecksSection`, treat an empty bucket
  as *still loading* whenever the page-level query has more to give: pass
  `hasNextPage || isFetchingNextPage` down (e.g. fold it into the existing
  `isLoading` prop) and render the skeleton rows, never `noChecks`, while it
  is true. Once the stream is fully drained (`hasNextPage === false`), the
  empty state is truthful and renders as today.
- Give the sentinel's IntersectionObserver a `rootMargin` of roughly two
  viewports (e.g. `"1200px 0px"`) so the next page starts loading well before
  the user reaches the bottom. The observer already re-fires on reconnect
  (the effect re-runs when `isFetchingNextPage` flips), so draining
  self-chains while the sentinel stays within the margin.

### Part 2 — load in display order (`sort=group`)

Add an opt-in `sort=group` query parameter to `GET
/api/v1/orgs/:org/checks` so pages arrive in the exact order the page
renders them:

- Ordering: group `sort_order` ascending with ungrouped checks **last**
  (matching `UngroupedChecksSection` rendering below all groups), then
  `created_at DESC, uid DESC` within a bucket. Requires a LEFT JOIN on
  `check_groups` (or a correlated subquery) in `ListChecks`.
- Cursor: becomes composite — (effective group sort key, created_at, uid) —
  encode the group sort key into the cursor alongside the existing
  created_at/uid pair. Use a sentinel (e.g. `COALESCE(g.sort_order,
  <max>)`-style effective key) so NULL/ungrouped sorts last identically on
  PostgreSQL and SQLite; do not rely on `NULLS LAST` support differences.
- Implement in **both** stores (`server/internal/db/postgres/postgres.go:1309`
  and the sqlite twin) — keep them in parity, and cover with table-driven
  store tests: page boundaries falling inside a group, exactly on a group
  boundary, ungrouped-last, and a group whose `sort_order` changes between
  pages (acceptable to document as best-effort, like any cursor pagination
  under concurrent reorder).
- Default ordering stays `created_at DESC` — `sort=group` is opt-in so no
  other consumer (MCP tools, dashboard widgets, exports) changes behavior.
- Frontend: `useInfiniteChecks` gains a `sort` option; the checks index page
  passes `sort: "group"`. With the stream ordered this way, what is above
  the fold loads first, and once the stream has advanced past a group's sort
  position an empty bucket is provably empty — so Part 1's skeleton can be
  narrowed later if desired (not required for this spec).

### Explicitly rejected

Auto-draining all pages in the background (`useEffect` looping
`fetchNextPage`): a one-liner, but every live `checks`/`results` hint
invalidates the infinite root and refetches **all** loaded pages, so a
fully-drained 800-check org would re-issue 8 requests per live tick, and the
page would render every row with no virtualization. Fine as an emergency
stopgap, not the fix.

### Testing

- Store tests (PG + SQLite) for `sort=group` ordering and composite-cursor
  page walks as above.
- Handler test: `sort=group` accepted, unknown `sort` value rejected with
  `VALIDATION_ERROR`, default unchanged.
- Playwright E2E (`web/dash0/e2e/`): seed > 1 page of checks across several
  groups (respecting the 100 cap — e.g. limit override or >100 seeded
  checks), assert that on first paint no group section with a non-zero badge
  shows the "No checks" text (skeleton or rows only), and that after scroll
  the top group's rows are present. Guard the false-empty regression
  specifically: a group whose checks are the *oldest* must not show
  `noChecks` while `hasNextPage` is true.

## Implementation Plan

### Part 1 — frontend false-empty-state fix (checks.index.tsx)

1. In `ChecksIndexPage`, derive `checksStreaming = checksLoading || hasNextPage
   || isFetchingNextPage` and pass it as the `isLoading` prop to both
   `CheckGroupSection` and `UngroupedChecksSection` (replacing the bare
   `checksLoading`). "Still could receive more rows" now reads as loading.
2. Reorder the render conditionals in both sections so **rows win over the
   loading branch**: `error ? … : checks.length > 0 ? <ChecksTable> :
   isLoading ? <skeletons> : <empty>`. This keeps already-loaded rows visible
   while the stream continues, shows skeletons for a not-yet-reached bucket,
   and only renders the truthful empty state once the stream is fully drained.
3. `UngroupedChecksSection` keeps its `if (!isLoading && !error &&
   checks.length === 0 && !search) return null;` early-return — with the new
   streaming-inclusive `isLoading` it stays mounted (skeletons) during the
   drain and collapses to `null` only once drained with nothing ungrouped.
4. Give the sentinel's `IntersectionObserver` `rootMargin: "1200px 0px"`
   (~two viewports) so the next page prefetches before the user reaches the
   bottom. Keep the self-chaining effect (re-runs when `handleObserver`
   identity changes on `isFetchingNextPage` flip).

### Part 2 — backend `sort=group` opt-in ordering

Effective group sort key (both stores, identical SQL, no `NULLS LAST`
reliance): `COALESCE((SELECT g.sort_order FROM check_groups AS g WHERE g.uid =
check_group_uid), 2147483647)` — a correlated subquery referencing the outer
`checks.check_group_uid` by bare column name (avoids the reserved-word `check`
default bun alias). `sort_order` is `int16` (max 32767) so the `2147483647`
sentinel always sorts ungrouped strictly last.

1. **Model** (`db/models/check.go`): add `GroupSortKey int64
   \`bun:"group_sort_key,scanonly"\`` (transient scan field, only populated in
   sort=group mode). **Filter** (`ListChecksFilter`): add `SortByGroup bool`
   and `CursorGroupSortKey *int64`.
2. **postgres + sqlite `ListChecks`** (parity): move the `Order(...)` out of the
   builder head. After the filter block, if `SortByGroup`: select `*` plus the
   sort-key expr `AS group_sort_key`, order `group_sort_key ASC, created_at
   DESC, uid DESC`; else keep `created_at DESC, uid DESC`. Cursor predicate for
   sort=group is the composite keyset: `(key > c.key) OR (key = c.key AND
   created_at < c.ca) OR (key = c.key AND created_at = c.ca AND uid < c.uid)`
   (whole thing wrapped in one parenthesised `.Where`).
3. **service** (`handlers/checks/service.go`): `ListChecksOptions.Sort string`;
   set `filter.SortByGroup = opts.Sort == "group"`. Branch cursor decode
   (`decodeGroupCursor` when sorting by group) and next-cursor encode
   (`encodeGroupCursor(lastCheck.GroupSortKey, createdAt, uid)`). New helpers
   `encodeGroupCursor`/`decodeGroupCursor` add the group key as a leading
   pipe-delimited field; the default `encodeCursor`/`decodeCursor` stay
   byte-for-byte unchanged so no other consumer's cursor changes.
4. **handler** (`handlers/checks/handler.go`): parse `sort` — accept `"group"`,
   reject any other non-empty value with `VALIDATION_ERROR` (400). Empty/absent
   → default ordering unchanged.
5. **OpenAPI**: document the `sort` query param on `listChecks`.
6. **frontend** (`api/hooks.ts`): `buildChecksUrl` + `useInfiniteChecks` gain a
   `sort?: string` option; `checks.index.tsx` passes `sort: "group"`.

### Testing

- **Store tests** (postgres embedded + sqlite in-memory, mirrored): `sort=group`
  ordering (group sort_order asc, ungrouped last, created_at DESC/uid DESC
  within a bucket) and composite-cursor page walks — boundary inside a group,
  exactly on a group boundary, ungrouped-last, and a group whose `sort_order`
  changes between pages (documented best-effort).
- **Handler test** (sqlite): `sort=group` accepted and orders by group;
  unknown `sort=bogus` → 400 `VALIDATION_ERROR`; default (no `sort`) unchanged.
- **Playwright E2E** (`checks-index-group-pagination.spec.ts`): 3 groups with
  explicit `sortOrder` 0/2/4, >100 label-scoped checks bulk-imported (A=60,
  B=42, C=8). Deep-link with the label so the infinite query returns exactly
  the seeded set (2 pages). Assert the last group (C, empty on page 1) shows
  **skeletons, never "No checks"** while `hasNextPage` is true; scroll to
  drain; assert C's rows then appear.
