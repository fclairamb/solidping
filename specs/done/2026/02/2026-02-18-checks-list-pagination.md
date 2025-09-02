# Checks Listing API: Cursor Pagination, Limit & Search

## Overview

Update the `GET /api/v1/orgs/$org/checks` endpoint to support cursor-based pagination, configurable result limits, and search filtering by name/slug. This brings the checks API in line with the existing results and incidents APIs which already support pagination.

## Motivation

1. The checks listing currently returns all checks with no pagination, which becomes inefficient as the number of checks grows.
2. Users need to search/filter checks by name or slug without fetching all of them.
3. Consistency with the results and incidents APIs which already use cursor-based pagination.

## Design Decisions

- **Cursor strategy**: Use `(created_at, uid)` encoded as base64. The `uid` tiebreaker ensures stable pagination when multiple checks share the same `created_at` timestamp. The checks table has no index on `(organization_uid, created_at)`, but checks-per-org counts are bounded and small (typically < 1000), so a sequential scan filtered by `organization_uid` + in-memory sort is efficient. No new index is needed.
- **Search**: Case-insensitive substring match on both `name` and `slug` fields using `LOWER(col) LIKE '%q%'` (works on both PostgreSQL and SQLite).
- **Limit**: Default 20, max 100, consistent with the results API.
- **Total count**: Returned in the pagination response via a separate `COUNT(*)` query with the same filters (excluding cursor/limit).
- **Backward compatibility**: Existing query parameters (`with`, `labels`) continue to work unchanged. The response now includes a `pagination` field alongside `data`.

## API

### `GET /api/v1/orgs/$org/checks`

#### New Query Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `cursor` | string | (none) | Opaque cursor from a previous response |
| `limit` | int | 20 | Number of results per page (1-100) |
| `q` | string | (none) | Search filter matching name and slug (case-insensitive substring) |

#### Updated Response Format

```json
{
  "data": [
    {
      "uid": "...",
      "name": "My HTTP Check",
      "slug": "my-http-check",
      "type": "http",
      "enabled": true,
      "period": "00:01:00",
      "createdAt": "2026-02-18T10:00:00Z"
    }
  ],
  "pagination": {
    "total": 150,
    "cursor": "base64-encoded-string",
    "limit": 20
  }
}
```

- `pagination.cursor` is only present when there are more results beyond the current page.
- `pagination.total` is the total count of checks matching all filters (before pagination).
- `pagination.limit` reflects the actual limit used.

---

## 1. Model Update

**File**: `back/internal/db/models/check.go`

Add fields to `ListChecksFilter`:

```go
type ListChecksFilter struct {
    Labels          map[string]string
    Query           string
    Limit           int
    CursorCreatedAt *time.Time
    CursorUID       *string
}
```

---

## 2. Database Interface Update

**File**: `back/internal/db/service.go`

Change `ListChecks` signature to return total count:

```go
ListChecks(ctx context.Context, orgUID string, filter *models.ListChecksFilter) ([]*models.Check, int64, error)
```

---

## 3. Database Implementation Updates

**Files**: `back/internal/db/postgres/postgres.go`, `back/internal/db/sqlite/sqlite.go`

Build the query with:
- Search: `WHERE (LOWER(name) LIKE ? OR LOWER(slug) LIKE ?)` with `%q%` pattern
- Cursor: `WHERE (created_at < ? OR (created_at = ? AND uid < ?))` for keyset pagination
- Limit: `LIMIT filter.Limit + 1` (fetch one extra to detect "has more")
- Count: separate `COUNT(*)` query with the same filters (no cursor/limit)

---

## 4. Service Layer Update

**File**: `back/internal/handlers/checks/service.go`

- Add `Query`, `Cursor`, `Limit` fields to `ListChecksOptions`
- Add `ListChecksResponse` and `PaginationResponse` structs
- Add `encodeCursor`/`decodeCursor` methods (base64 of `RFC3339Nano|uid`)
- Update `ListChecks` to pass filter fields, detect "has more", build cursor, return `*ListChecksResponse`

---

## 5. Handler Update

**File**: `back/internal/handlers/checks/handler.go`

Parse new query parameters:
- `cursor` - string, passed through
- `limit` - int, validate 1-100, default 20
- `q` - string, passed through

Return `ListChecksResponse` directly (includes `data` and `pagination`).

---

## 6. Integration Tests

**File**: `back/internal/db/service_test.go`

Test cases:
- **Pagination**: create N checks, list with limit < N, verify cursor returned, use cursor to get next page, verify no overlap and all checks found
- **Limit**: verify exact count returned
- **Search**: create checks with known names/slugs, filter with `q`, verify only matching checks returned (case-insensitive)
- **Combined**: pagination + search together

---

## Files to Modify/Create

| File | Action |
|------|--------|
| `back/internal/db/models/check.go` | Modify: add fields to `ListChecksFilter` |
| `back/internal/db/service.go` | Modify: update `ListChecks` signature |
| `back/internal/db/postgres/postgres.go` | Modify: add pagination, search, count |
| `back/internal/db/sqlite/sqlite.go` | Modify: same as postgres |
| `back/internal/handlers/checks/service.go` | Modify: pagination types, cursor, updated `ListChecks` |
| `back/internal/handlers/checks/handler.go` | Modify: parse `cursor`, `limit`, `q` |
| `back/internal/db/service_test.go` | Modify: add integration tests |

## Verification

1. Build: `cd back && go build ./...`
2. Test: `cd back && go test ./...`
3. Manual API testing:
   ```bash
   TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' 'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

   # Default pagination
   curl -s -H "Authorization: Bearer $TOKEN" 'http://localhost:4000/api/v1/orgs/default/checks'

   # With limit
   curl -s -H "Authorization: Bearer $TOKEN" 'http://localhost:4000/api/v1/orgs/default/checks?limit=2'

   # With search
   curl -s -H "Authorization: Bearer $TOKEN" 'http://localhost:4000/api/v1/orgs/default/checks?q=http'

   # With cursor from previous response
   curl -s -H "Authorization: Bearer $TOKEN" 'http://localhost:4000/api/v1/orgs/default/checks?cursor=...'
   ```
