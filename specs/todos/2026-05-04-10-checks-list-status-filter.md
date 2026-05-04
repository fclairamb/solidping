# Filter checks list by current status (up/down/error/timeout)

## Context

The dash0 checks list at `https://solidping.k8xp.com/dash0/orgs/<org>/checks` cannot be filtered by current status. With dozens of checks, finding the failing ones means scanning. The data is already on the wire — `Check.lastResult.status` is populated and rendered as a colored dot — so this is purely a missing filter on top of existing data.

The label-filter UI shipped in `specs/done/2026/05/2026-05-02-23-labels-filter-checks-list.md` provides the pattern to mirror: multi-select chips, URL-state via TanStack Router's `useSearch()`, query-param threading through to the backend.

## Scope

**In scope:**
- Backend: `GET /api/v1/orgs/:org/checks` accepts `status=up,down,error,timeout,created` (comma-separated, multi-value, singular field name per repo convention).
- DB layer: `ListChecksFilter` (`server/internal/db/models/check.go:173-182`) gets a `Statuses []string` field; SQL builder filters on the latest result's status.
- Frontend: a status filter widget on `web/dash0/src/routes/orgs/$org/checks.index.tsx`, mirroring the labels filter UX. Persist in URL.
- Tests on both sides.

**Out of scope:**
- Saved-filter views.
- "Show only checks currently in incidents" — different concept (an incident may exist briefly while status flips); separate spec.
- Filter on incident state, threshold, or thresholds — future.

## Approach

### 1. Backend

`server/internal/handlers/checks/handler.go:100-132` — parse `?status=` like the existing `?labels=`:

```go
if statusParam := r.URL.Query().Get("status"); statusParam != "" {
    filter.Statuses = strings.Split(statusParam, ",")
    // optional: validate each is a known status; reject others as VALIDATION_ERROR
}
```

`server/internal/db/models/check.go:173-182`:
```go
type ListChecksFilter struct {
    ...
    Statuses []string
}
```

Find the SQL builder for `ListChecks` (likely in `server/internal/db/sqlite/checks.go` and `server/internal/db/postgres/checks.go`). The existing `lastResult` join (used to populate the dashboard's status dot) gives us the comparison column. Append:

```sql
AND lr.status IN (?, ?, ?)
```

…with placeholders generated from `Statuses`. If the join is currently a LEFT JOIN that produces NULL for "never run" checks and we add the filter, a check with no last result will be excluded — that's correct only if "created" is in the user's selected set. Map the SQL `lr.status IS NULL` case to the `created` filter token to match the UI's mental model.

Validate accepted values server-side: `{up, down, error, timeout, created}`. Reject others with `VALIDATION_ERROR`.

### 2. Frontend

`web/dash0/src/api/hooks.ts:219-236` — `useInfiniteChecks` adds a `statuses?: string[]` arg, joins with `,`, threads to the URL.

`web/dash0/src/routes/orgs/$org/checks.index.tsx`:
- Add a status filter component near the existing search box / labels filter (~line 750). Reuse the same chip + popover pattern.
- Options: Up (green), Down (red), Error (orange), Timeout (yellow), Created (grey).
- Persist via `useSearch()` route search params: `status: z.array(z.enum([...])).optional()`.
- Pass to `useInfiniteChecks({ ..., statuses: search.status })`.

### 3. Tests

**Backend** (`server/internal/handlers/checks/handler_test.go`):
- `?status=down` returns only checks whose last result is `down`.
- `?status=down,error` returns the union.
- `?status=created` includes checks with no last result.
- `?status=invalid` → 400 with `VALIDATION_ERROR`.

**Frontend** (Playwright `web/dash0/e2e/checks-status-filter.spec.ts`):
1. Seed checks: one up, one down, one created.
2. Visit list — three rows visible.
3. Click status filter, select "Down" — one row.
4. URL contains `?status=down`.
5. Reload — filter persists.

## Verification

1. `make test` and `make test-dash` pass.
2. Manually: list page with mixed-status checks → toggling Down shows only failing checks; URL updates.
3. Backend: `curl -H "Authorization: Bearer $TOKEN" 'http://localhost:4000/api/v1/orgs/default/checks?status=down'` returns only down checks.
