# Check result detail: previous / next result navigation

## Problem

On the result detail page
(`/orgs/$org/checks/$checkUid/results/$resultUid`, e.g.
`/dash0/orgs/webingenia/checks/4e52ef3c-.../results/019f33d8-...`), the only
navigation is the back arrow to the check page
(`web/dash0/src/routes/orgs/$org/checks.$checkUid.results.$resultUid.tsx:135-148`).

Walking through a sequence of results — the natural move when diagnosing a
latency spike or an error window — currently means: back to the check page,
find the adjacent row in Recent Results, click it, repeat. Three interactions
and a full page swap per step. Worse, Recent Results only lists the newest 10
rows (`checks.$checkUid.index.tsx:341-346`), so once you are more than 10
results back, the table can't take you to the neighbor at all (the only other
entry point is pinning a chart point,
`web/dash0/src/components/checks/pinned-result-box.tsx:165`).

The page should have **Previous / Next** buttons that step chronologically
through the check's results.

The API currently offers no way to find a result's neighbors:

- The detail endpoint returns a single row
  (`GET /api/v1/orgs/{org}/checks/{check}/results/{uid}`,
  `server/internal/handlers/results/service.go:418-475`).
- The list endpoint is keyset-paginated strictly newest → oldest
  (`ORDER BY period_start DESC, uid DESC` —
  `server/internal/db/postgres/postgres.go:1732`,
  `server/internal/db/sqlite/sqlite.go:1643-1644`). The "older" neighbor
  could be synthesized by forging a cursor from the current row, but the
  cursor encoding is an opaque implementation detail
  (`service.go:229-252`), and there is no ascending order at all, so the
  "newer" neighbor is unreachable.

## Proposal

### Backend: neighbor UIDs on the detail response

1. **Extend `GetResultResponse`** (`service.go:410-413`) with two optional
   fields:

   ```json
   { "previousUid": "…", "nextUid": "…" }
   ```

   `previousUid` = the next-**older** result, `nextUid` = the next-**newer**
   one. Omitted (`omitempty`) when the current row is the oldest/newest —
   the frontend disables the matching button.

2. **Neighbor scope**: same organization + check + `periodType` as the
   **effective returned row**. Computing from the effective row (not the
   requested UID) means a fallback response — raw UID rolled up into an
   aggregation (`service.go:452-472`) — navigates through the aggregation
   series at that level, where every step has a real row UID and loads
   without the fallback banner.

3. **Neighbor queries**: one new `db.Service` method (e.g.
   `GetResultNeighbors(ctx, orgUID, checkUID, periodType string, regions
   []string, pivotStart time.Time, pivotUID string)` returning
   `(prevUID, nextUID string)`), implemented in **both**
   `server/internal/db/postgres/postgres.go` and
   `server/internal/db/sqlite/sqlite.go` (keep the two in sync):

   - previous: `(period_start, uid) < (pivot)` →
     `ORDER BY period_start DESC, uid DESC LIMIT 1`
   - next: `(period_start, uid) > (pivot)` →
     `ORDER BY period_start ASC, uid ASC LIMIT 1`

   Reuse the expanded keyset idiom already used by the list cursor
   (`postgres.go:1773-1774`:
   `(period_start < ?) OR (period_start = ? AND uid < ?)`; sqlite
   equivalent around `sqlite.go:1683`). The `uid` tie-break makes
   same-timestamp rows (multi-region runs landing in the same ms) a stable
   total order with no skips, and UUIDv7 makes it creation-ordered.

4. **Optional `region` query param** on the GET detail endpoint
   (`server/internal/handlers/results/handler.go:136-139`), comma-separated
   like the list endpoint's (`handler.go:54-56`). It constrains the
   **neighbor scope only** — the row itself is still fetched by UID. Without
   it, neighbors are chronological across all regions, matching the
   unfiltered Recent Results table; with it, stepping stays inside the
   filtered region(s).

5. **OpenAPI** (`server/internal/app/openapi/openapi.yaml`): add
   `previousUid` / `nextUid` to the result detail response schema and the
   `region` query parameter to the detail operation.

### Frontend

Per the repo rule, start from the design reference
(`web/dash0/src/routes/orgs/$org/design-reference.tsx`) — the ghost icon
button used below already exists there (and on this very page as the back
arrow), so no new primitive is needed.

1. **Route search param**: add a `validateSearch` to the detail route
   (`checks.$checkUid.results.$resultUid.tsx:17-21`) with an optional
   `region: string` param. It is forwarded to the API call and preserved
   when navigating prev/next.

2. **`useResult` hook** (`web/dash0/src/api/hooks.ts:689-699`): accept an
   optional `region` option, append it to the request URL and include it in
   the `queryKey`. Add `previousUid` / `nextUid` to the `OrgResultDetail`
   type.

3. **Header buttons**: in the header row (`…results.$resultUid.tsx:135-158`),
   add a right-aligned pair of ghost icon buttons (`ml-auto flex gap-1`):
   `ChevronLeft` → navigate to `previousUid` (older), `ChevronRight` →
   navigate to `nextUid` (newer). Each disabled when its UID is absent.
   Tooltips + `aria-label`s from new i18n keys
   (`checks:resultDetail.previousResult` / `nextResult`) added to all four
   locales (`web/dash0/src/locales/{de,en,es,fr}`). Navigation goes to the
   same route with the new `resultUid` param, keeping `search` (the region
   filter). Buttons keep working step after step because every detail
   response carries its own neighbors.

4. **Semantics**: left chevron = back in time (older), right chevron =
   forward in time (newer). Label them "Previous result" / "Next result".

5. **Staleness note**: `useResult` caches with `staleTime: Infinity`
   (`hooks.ts:697`) because result rows are immutable — but `nextUid` is
   not: the newest result gains a newer neighbor once the next run lands.
   Accept this (the disabled Next on the newest result is momentarily
   conservative, never wrong-linking); no `staleTime` change required.

### Sequencing

Spec **2026-07-05-13** (in progress on
`feat/per-region-chart-series-results-filter-stats`) is mid-edit in the same
files (`results/service.go`, `service_test.go`, `openapi.yaml`) and adds the
Recent Results region filter. Implement this spec **after** it lands; then
the region-filtered table links can pass their active region into the detail
route's `region` search param so prev/next stepping stays in-region.

## Out of scope

- **Cross-periodType hopping**: stepping older than the oldest retained raw
  row disables Previous rather than jumping into the hour aggregates (and
  likewise at every aggregation level). Mixing levels has no well-defined
  order (a raw row's instant lies *inside* an aggregation bucket).
- **Keyboard ←/→ shortcuts** — cheap polish, may ride along if trivial, not
  required.
- **Prev/next inside the pinned chart box**
  (`pinned-result-box.tsx`) — the chart itself already offers
  adjacent-point browsing.
- **"Result N of M" position counter** — would need a COUNT per page load
  for little value.

## Acceptance criteria

1. `GET …/results/{uid}` returns `previousUid` / `nextUid`; both omitted at
   the respective boundary; scope is same check + periodType; `region`
   query param narrows the scope; a rolled-up (fallback) response returns
   neighbors from the covering aggregation's series; two rows sharing
   `period_start` are ordered by `uid` with no skip or repeat when stepping
   across them.
2. Service tests (`server/internal/handlers/results/service_test.go`):
   middle / oldest / newest rows, periodType isolation, other-check and
   other-org isolation, region scoping, same-timestamp tie-break, fallback
   case — passing on both SQLite and Postgres backends (`make test`).
3. Detail page shows the two buttons; each disabled exactly when its UID is
   absent; clicking steps to the neighbor and the buttons stay live across
   steps; the `region` search param survives navigation. Playwright e2e in
   `web/dash0/e2e/` seeds a check with ≥2 results, opens the newest (Next
   disabled), steps back and forward again.
4. OpenAPI schema updated; `make lint` and `make test` pass; all four
   locale files carry the new keys (no hardcoded English).

## Implementation Plan

Verified against `main` post-spec-13: `service.go` is 538 lines, `GetResult`
at 418-475, `GetResultResponse` at 407-413, `findCoveringAggregation` at
477-538 (unchanged shape from the spec's line citations, just shifted a few
lines). `postgres.go`/`sqlite.go` `ListResults` bodies are byte-identical;
cursor tie-break idiom at `postgres.go:1773-1774` /
`sqlite.go:1684` exactly as cited.

1. **`db.Service.GetResultNeighbors`** (`server/internal/db/service.go`,
   grouped in the "Result operations" block right after `ListResults`):
   ```go
   GetResultNeighbors(
       ctx context.Context, orgUID, checkUID, periodType string, regions []string,
       pivotStart time.Time, pivotUID string,
   ) (prevUID, nextUID string, err error)
   ```
   `""` UID = no neighbor in that direction (boundary). Implemented
   identically in `postgres.go`/`sqlite.go` (both bun, both dialect-agnostic
   SQL — no per-backend divergence needed, matching every other Result
   method in these two files): two scoped `SELECT uid` queries reusing the
   `(period_start < ?) OR (period_start = ? AND uid < ?)` keyset idiom (DESC
   for previous, mirrored ASC/`>` for next), each `.Where("region IN (?)",
   ...)` only when `regions` is non-empty, `sql.ErrNoRows` mapped to `""`
   rather than propagated.

2. **Tests**: new `testGetResultNeighbors(ctx, t, svc db.Service)` helper in
   `server/internal/db/service_test.go`, registered in `testService`'s
   `t.Run` list — runs automatically against `TestPostgresService`
   (embedded-postgres, `-short`-skipped), `TestSQLiteService`, and
   `TestSQLiteServiceInMemory`. This is the repo's established dual-backend
   pattern for new `db.Service` methods (no per-package
   `_postgres_test.go`/mocks needed here — only `postgres.go` and
   `sqlite.go` implement `db.Service`).

3. **`GetResultResponse`**: add `PreviousUID`/`NextUID string
   \`json:"previousUid,omitempty"\`` fields. `GetResult`
   (`server/internal/handlers/results/service.go`) computes them after
   resolving the effective row (direct hit or fallback aggregation) — same
   org/check/periodType as that row, using its `PeriodStart`/`UID` as pivot.
   Signature grows an optional `regions []string` parameter threaded from
   the handler's new `region` query param.

4. **Handler + OpenAPI**: `region` query param on `GetResult`
   (`server/internal/handlers/results/handler.go`), comma-separated like the
   list endpoint. OpenAPI has **no existing path entry at all** for
   `GET .../checks/{check}/results/{uid}` (only the list endpoint
   `/api/v1/orgs/{org}/results` is documented) — add a new path
   `/api/v1/orgs/{org}/checks/{check}/results/{uid}` plus a
   `GetOrgResultResponse` schema (extends `OrgResult` with `fallback`,
   `previousUid`, `nextUid`), following the existing `/checks/{check}/
   availability` path as a structural template.

5. **Frontend**: `region?: string` on the detail route's `validateSearch`;
   `useResult` gains an optional `region` param (URL + query key);
   `OrgResultDetail` gains `previousUid?`/`nextUid?`. Header gets a
   right-aligned ghost-icon `ChevronLeft`/`ChevronRight` pair (pattern
   already in `design-reference.tsx`'s back-arrow example), each disabled
   when its UID is absent, wrapped in `Tooltip`/`TooltipContent` (also
   already cataloged) with new `checks:resultDetail.previousResult` /
   `.nextResult` i18n keys in all 4 locales. The Recent Results row's
   `onClick` in `checks.$checkUid.index.tsx` currently navigates with no
   `search` at all — add `search: { region: resultsRegion }` so the active
   filter carries into the detail page and back out through prev/next.

6. **E2E**: new `web/dash0/e2e/check-result-detail-navigation.spec.ts`,
   following `check-chart-point-preview.spec.ts`'s route-mocking convention
   (mock the `/checks/*/results/{uid}` detail endpoint per-UID with
   different `previousUid`/`nextUid`, rather than driving real aggregation)
   — seeds a check, opens the newest result (Next disabled), steps back
   (Previous still enabled if applicable) and forward again, and confirms
   the `region` search param round-trips.

Commit granularity: (1) DB method both backends — done; (2) service.go
neighbor computation + handler region param + service tests; (3) OpenAPI;
(4) frontend route/hook; (5) header buttons + i18n; (6) E2E + `make fmt`
sweep; (7) all-checks-passing commit.
