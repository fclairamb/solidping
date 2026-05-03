# Labels — autocomplete API endpoint

## Context

Labels are already fully implemented on the backend (`labels` table + `check_labels` junction in `server/internal/db/postgres/migrations/001_initial.up.sql:263-292`, model in `server/internal/db/models/check.go:115-153`, DB methods in `server/internal/db/postgres/postgres.go:1219-1328`, create/update/list-with-filter wired through `server/internal/handlers/checks/`). The `GET /api/v1/orgs/$org/checks?labels=k:v,k2:v2` filter works today.

What is missing: **a way for the dashboard to discover existing keys/values** so users get autocomplete when adding or filtering by labels. Without it, the upcoming `<LabelInput>` component would have to either (a) derive suggestions from already-loaded check rows (incomplete and slow once you paginate) or (b) ship blind — no suggestions at all.

This spec adds a single small endpoint that powers autocomplete for both the check edit form (spec `2026-05-02-03`) and the checks list filter (spec `2026-05-02-04`). Both UI consumers depend on this; ship it first.

## Honest opinion

One endpoint with an optional `key` parameter is enough. Two separate endpoints (`/labels/keys`, `/labels/keys/{key}/values`) would be more REST-pure but they'd return data of the exact same shape (`[{value, count}]`), and the UI's two consumers (key combobox, value combobox) just need to swap the URL — there's no behavioural divergence to model. One endpoint, one shape, one DB query path.

I'm including a `count` (how many checks use this key/value) so the UI can sort most-used first. It's cheap (already in the GROUP BY) and turns autocomplete from alphabetical-noise into "what people actually use here".

## Scope

**In:**
- New endpoint `GET /api/v1/orgs/$org/labels` returning distinct keys, or distinct values for a given key.
- Two new methods on the DB service (Postgres + SQLite — both backends are maintained per `server/internal/db/sqlite/sqlite.go:1280` having parallel label code).
- New handler package `server/internal/handlers/labels/`.
- Wire route in `server/internal/app/server.go` alongside other org-scoped routes.
- Integration tests.
- Update `docs/api-specification.md`.

**Out:**
- Frontend UI (covered in specs `03` and `04`).
- Label management UI (rename/delete a label across all checks). Not needed for autocomplete; revisit only if users ask.
- OR-logic filtering on the existing list endpoint. Separate concern.
- Adding labels to other entities (CheckGroup / StatusPage / MaintenanceWindow). Out of scope per planning decision.

## Endpoint

```
GET /api/v1/orgs/$org/labels
```

**Query params:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `key` | string | — | If omitted, response lists distinct keys. If provided, response lists distinct values for that key. |
| `q`   | string | — | Case-insensitive prefix filter applied to the returned `value`. Empty/missing = no filter. |
| `limit` | int | 50 | 1–200. Clamped silently above 200 (mirrors `ListChecks` limit handling at `server/internal/handlers/checks/handler.go:101-117`). |

**Response:**

```json
{
  "data": [
    {"value": "department", "count": 12},
    {"value": "team",       "count": 8}
  ]
}
```

- `value`: either the label key (when `key` not supplied) or the label value (when `key` is supplied).
- `count`: number of distinct **checks** carrying that key (or that key/value pair). Sorted DESC by `count`, then ASC by `value` for stable ties.
- Always wrapped in `data` per the project's API conventions in `CLAUDE.md`.
- If `key` is provided and matches no labels: `{"data": []}` (200), not 404. Autocomplete consumers expect "no suggestions", not an error.

**Errors:**
- `401 UNAUTHORIZED` — no/invalid token (handled by existing middleware).
- `403 FORBIDDEN` — user not a member of `$org` (handled by existing middleware).
- `404 ORGANIZATION_NOT_FOUND` — org slug doesn't resolve (handled by existing middleware).
- `400 VALIDATION_ERROR` — `limit` not an int. Use `base.ErrorCodeValidationError`.

**No rate limiting beyond what other org-scoped endpoints already have.** Autocomplete fires on debounced keystrokes; the underlying queries are cheap (indexed on `(organization_uid, key)`).

## DB methods (`server/internal/db/postgres/postgres.go`)

Add right after the existing label methods (after line 1328):

```go
// ListDistinctLabelKeys returns distinct label keys used by checks in the org,
// sorted by usage count DESC then key ASC. Filters by case-insensitive prefix
// on key when q != "".
func (s *Service) ListDistinctLabelKeys(
    ctx context.Context, orgUID, q string, limit int,
) ([]LabelSuggestion, error)

// ListDistinctLabelValues returns distinct values for a given label key in the
// org, sorted by usage count DESC then value ASC. Filters by case-insensitive
// prefix on value when q != "".
func (s *Service) ListDistinctLabelValues(
    ctx context.Context, orgUID, key, q string, limit int,
) ([]LabelSuggestion, error)
```

Where `LabelSuggestion` is a small struct (define in `server/internal/db/models/check.go` next to `Label`):

```go
type LabelSuggestion struct {
    Value string
    Count int
}
```

**SQL shape (keys):**

```sql
SELECT l.key AS value, COUNT(DISTINCT cl.check_uid) AS count
FROM labels l
JOIN check_labels cl ON cl.label_uid = l.uid
WHERE l.organization_uid = ?
  AND l.deleted_at IS NULL
  AND (? = '' OR l.key ILIKE ? || '%')
GROUP BY l.key
ORDER BY count DESC, l.key ASC
LIMIT ?
```

(`q` is passed twice: once for the empty check, once as the prefix. Bun handles both placeholders.)

**SQL shape (values):** same structure with `WHERE l.key = ?` added and `l.value` instead of `l.key` in `SELECT`/`GROUP BY`/`ORDER BY`.

**Important:** the join through `check_labels` matters — we deliberately exclude orphaned labels (rows in `labels` not currently attached to any check). Autocomplete should suggest things in active use, not historical drift.

Mirror these methods in `server/internal/db/sqlite/sqlite.go` (SQLite uses `LIKE` instead of `ILIKE`; wrap with `LOWER(...)` on both sides).

## Handler package (`server/internal/handlers/labels/`)

Two files, mirror the layout in `server/internal/handlers/checks/`:

**`handler.go`** — `Handler` struct embedding `base.HandlerBase`, single method `ListLabels(writer, req)`:
- Read `org` path param.
- Parse `key`, `q`, `limit` query params (limit handling identical to `checks/handler.go:101-117`).
- Resolve org UID via `s.svc` (use the same pattern other handlers use for org slug → UID).
- Call `s.svc.ListLabels(ctx, orgUID, key, q, limit)`.
- Return `WriteJSON(writer, 200, struct{Data []SuggestionResponse `json:"data"`}{...})`.

**`service.go`** — `Service` struct holding `db db.Service`. Single method:

```go
func (s *Service) ListLabels(
    ctx context.Context, orgUID, key, q string, limit int,
) ([]SuggestionResponse, error) {
    if key == "" {
        suggestions, err := s.db.ListDistinctLabelKeys(ctx, orgUID, q, limit)
        // map to response, return
    }
    return s.db.ListDistinctLabelValues(ctx, orgUID, key, q, limit) // map to response
}

type SuggestionResponse struct {
    Value string `json:"value"`
    Count int    `json:"count"`
}
```

## Route registration (`server/internal/app/server.go`)

Add to the imports:

```go
"github.com/fclairamb/solidping/server/internal/handlers/labels"
```

Construct the service and handler alongside the existing checks handler/service wiring (search for where `checks.NewHandler` is built — labels handler should sit next to it). Then register inside the org-scoped route group:

```go
g.GET("/labels", labelsHandler.ListLabels)
```

(Exact group identifier matches the surrounding pattern — copy from how `checks` routes are registered.)

## Tests

New file `server/test/integration/labels_test.go`. Follow the testify+testcontainers pattern from `server/test/integration/checks_test.go`. Each test calls `t.Parallel()` per `server/CLAUDE.md`.

Cases:

1. **Empty org → empty data.** Fresh org with no checks/labels → `GET /labels` → `{"data": []}`.
2. **Distinct keys with counts.** Create 3 checks: A has `{env: prod, team: web}`; B has `{env: prod}`; C has `{team: web}`. → `GET /labels` returns `[{value: "env", count: 2}, {value: "team", count: 2}]` (or sorted by key ASC after equal count). Assert order by count DESC, key ASC.
3. **Distinct values for a key.** Same setup → `GET /labels?key=env` → `[{value: "prod", count: 2}]`.
4. **Prefix filter.** Add label `{environment: staging}` → `GET /labels?q=env` → returns both `env` and `environment`. `GET /labels?q=envi` → returns only `environment`.
5. **Case-insensitive prefix.** `GET /labels?q=ENV` → returns same as `q=env`.
6. **Limit clamping.** Set `limit=500` → response capped at 200; the call still 200 OK (silent clamp).
7. **Limit invalid.** `?limit=foo` → 400 with `VALIDATION_ERROR`.
8. **Org isolation.** Create org A and org B with overlapping label keys; confirm `GET /api/v1/orgs/A/labels` only shows A's data. Use a non-member token against B → 403.
9. **Detached labels excluded.** Create a label via `GetOrCreateLabel` but don't attach it to any check (or detach via `SetCheckLabels` with empty list). Confirm it does NOT appear in suggestions.
10. **Soft-deleted labels excluded.** Manually mark a label `deleted_at`; confirm it doesn't appear.

For the auth/middleware-bound cases (1, 8) reuse the test harness in `server/test/integration/` (look at `checks_test.go` for the existing org/auth setup helpers).

## Documentation

Add to `docs/api-specification.md` next to the existing `labels` filter mention (around line 182):

- New section under org-scoped endpoints: `GET /api/v1/orgs/$org/labels` with the param table and example response from this spec.

## Verification

Backend dev server runs at port 4000 per `server/CLAUDE.md`.

```bash
# 1. Login
curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken' > /tmp/token.txt
TOKEN=$(cat /tmp/token.txt)

# 2. Seed: create checks with labels
for slug in alpha beta gamma; do
  curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$slug\",\"slug\":\"$slug\",\"type\":\"http\",\"config\":{\"url\":\"https://example.com\"},\"labels\":{\"environment\":\"prod\",\"team\":\"web\"}}" \
    "http://localhost:4000/api/v1/orgs/default/checks" >/dev/null
done

# 3. Distinct keys
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/labels' | jq '.'
# Expect: [{value: "environment", count: 3}, {value: "team", count: 3}]

# 4. Distinct values for a key
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/labels?key=environment' | jq '.'
# Expect: [{value: "prod", count: 3}]

# 5. Prefix filter
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/labels?q=env' | jq '.'
# Expect: [{value: "environment", count: 3}]

# 6. Run tests
make gotest

# 7. Lint
make lint-back
```

## Files touched

- `server/internal/db/models/check.go` — add `LabelSuggestion` struct.
- `server/internal/db/postgres/postgres.go` — add `ListDistinctLabelKeys`, `ListDistinctLabelValues`.
- `server/internal/db/sqlite/sqlite.go` — same two methods, SQLite syntax.
- `server/internal/db/db.go` (or wherever the `Service` interface lives — verify) — add the two methods to the interface so both backends satisfy it.
- `server/internal/handlers/labels/handler.go` — new.
- `server/internal/handlers/labels/service.go` — new.
- `server/internal/app/server.go` — wire handler + register route.
- `server/test/integration/labels_test.go` — new.
- `docs/api-specification.md` — document the endpoint.

No DB migration. No new dependency.

## Implementation Plan

1. Add `LabelSuggestion` struct to `server/internal/db/models/check.go`.
2. Add the two methods to the DB `Service` interface (locate it — likely `server/internal/db/db.go`; if there's no shared interface, just implement on both concrete types).
3. Implement `ListDistinctLabelKeys` and `ListDistinctLabelValues` in `postgres.go` after line 1328. Test manually with a `psql` query first to confirm the SQL is right.
4. Mirror in `sqlite.go` using `LOWER(...)` + `LIKE`.
5. Create `handlers/labels/{handler.go,service.go}` mirroring `handlers/checks/`.
6. Wire into `app/server.go` next to where checks routes are registered.
7. Write integration tests covering all 10 cases above. `make gotest` clean.
8. `make lint-back` clean.
9. Update `docs/api-specification.md`.
10. Smoke-test the curl flow above against `make dev`.
