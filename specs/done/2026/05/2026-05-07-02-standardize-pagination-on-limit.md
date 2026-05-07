# Standardize pagination parameter on `limit` across the REST API

## Context

The top-level `CLAUDE.md` REST conventions don't pick a name for the page-size
query parameter — they only require camelCase and singular form for
multi-value lists. In practice the codebase has split into two camps:

| Endpoint | Param | File:line |
|---|---|---|
| `GET /api/v1/orgs/$org/checks` | `limit` | `server/internal/handlers/checks/handler.go:139` |
| `GET /api/v1/orgs/$org/labels` | `limit` | `server/internal/handlers/labels/handler.go:43` |
| `GET /api/v1/orgs/$org/files` | `limit` | `server/internal/handlers/files/handler.go:48` |
| `GET /api/v1/orgs/$org/maintenance-windows` | `limit` | `server/internal/handlers/maintenancewindows/handler.go:42` |
| `GET /api/v1/orgs/$org/results` | `size` | `server/internal/handlers/results/handler.go:86` |
| `GET /api/v1/orgs/$org/events` | `size` | `server/internal/handlers/events/handler.go:62, 100, 138` |
| `GET /api/v1/orgs/$org/incidents` | `size` | `server/internal/handlers/incidents/handler.go:101` |

The split is roughly "list endpoints vs time-series endpoints", but neither
camp is wrong — there's just no documented preference, so contributors who
copy from `results` (when that was the first paginated endpoint) get `size`
and contributors who copy from `checks` get `limit`. CLI users, MCP tool
authors, and dashboard hooks all have to remember which endpoint takes
which name.

## Goal

Pick `limit`. Migrate the `size`-using endpoints to accept *both* `limit`
(canonical) and `size` (legacy alias) for one release, then drop `size` in
the following one. Update docs and OpenAPI so contributors copy from a
single example.

## Why `limit` and not `size`

- 4 endpoints already use `limit`; only 3 use `size`. Smaller migration.
- "Limit" is the dominant convention in REST: GitHub, Stripe, GitLab, AWS
  CLI — all of them use `limit` (or `per_page`). `size` is more common in
  Java/Spring shops, less so in the Go/Node ecosystem the rest of this API
  follows.
- `size` collides with the obvious "size of an upload" meaning on the files
  endpoint, where `?size=...` would be ambiguous between "page size" and
  "filter by file size".

## Approach

1. **Add `limit` as the canonical parameter on the three offenders** —
   `results`, `events`, `incidents` — and accept `size` as a legacy alias
   when `limit` is absent. Both keys map to the same internal variable;
   `limit` wins when both are present (with a debug-level log noting the
   conflict, so a misconfigured client gets a hint without being broken).
2. **Update `docs/api-specification.md`** to document only `limit` as the
   query parameter for every paginated endpoint. Add a one-line note on the
   migrated endpoints: `(also accepts ?size=, deprecated)`.
3. **Update `web/dash0/src/api/hooks.ts`** if any hook passes `size=` to the
   server (spot-check, since the dashboard is the largest internal client).
4. **Update CLI client and MCP tools** if either of them inlines `size=`
   anywhere — search `server/internal/cli` and `server/internal/mcp`.
5. **Add the convention to `CLAUDE.md`** under REST API conventions: "Use
   `limit` for the page-size query parameter; never `size`, `count`, or
   `pageSize`."
6. **Schedule the legacy-alias removal**: a follow-up spec dated one cycle
   from now drops the `size` fallback. Don't mix the removal with the
   add-`limit` change so each is independently revertable.

## Files to edit

### `server/internal/handlers/results/handler.go` (~line 86)

Replace:

```go
if sizeParam := query.Get("size"); sizeParam != "" {
    // ...
}
```

with:

```go
sizeParam := query.Get("limit")
if sizeParam == "" {
    sizeParam = query.Get("size") // legacy alias, deprecate next cycle
}
if sizeParam != "" {
    // ... existing parse + clamp logic unchanged
}
```

### `server/internal/handlers/events/handler.go` (lines 62, 100, 138)

Same replacement at all three sites — list events, list incident events,
list check events all paginate independently and all currently use `size`.
Extract a small helper into `events/handler.go` (or `base/`) so the
fallback logic is one-place: `parsePageLimit(query, defaultSize, maxSize) (int, error)`.

### `server/internal/handlers/incidents/handler.go` (line 101)

Same replacement at the single site.

### `docs/api-specification.md`

Find every endpoint section that mentions `?size=` and update to `?limit=`.
For the three migrated endpoints, add a single line after the `limit`
description:

> Also accepts `?size=` for backward compatibility — deprecated, will be
> removed in the next API revision. Send `?limit=` instead.

### `CLAUDE.md` (top-level, REST API choices section)

Add a bullet:

> - The page-size query parameter must be named `limit`. Default and max
>   values are per-endpoint; the name is not.

### `web/dash0/src/api/hooks.ts`

Grep for `size=` and `&size=` in the file. If hooks for results / events /
incidents construct URLs with `size`, change to `limit`. (No backward-compat
in the client; it's a same-repo change so the rename can be atomic on this
side.)

## Verification

1. `make test` — every existing pagination test should pass with `limit`
   without changes (the test bodies don't construct query strings; they use
   the handler's parameter parsing). Add a small targeted test for the new
   alias behavior:
   - `?limit=10` → returns 10.
   - `?size=10` → still returns 10 (alias).
   - `?limit=10&size=99` → returns 10 (`limit` wins).
2. Manual smoke via curl:
   ```bash
   TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
     -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
     http://localhost:4000/api/v1/auth/login | jq -r .accessToken)

   # New canonical
   curl -s -H "Authorization: Bearer $TOKEN" \
     'http://localhost:4000/api/v1/orgs/default/results?limit=5' | jq '.data | length'

   # Legacy alias still works
   curl -s -H "Authorization: Bearer $TOKEN" \
     'http://localhost:4000/api/v1/orgs/default/results?size=5' | jq '.data | length'
   ```
3. `make build-dash0` — TypeScript compile after dashboard hook updates.
4. Grep clean: `command grep -rn '"size"\|Get("size")' server/internal/handlers/`
   should only return matches inside the legacy-alias fallback blocks
   (one site per migrated endpoint, plus the helper if extracted).

## Out of scope

- Adding cursor-based pagination. The current API is offset/limit; cursors
  are a larger follow-on.
- Renaming other query params. `q` (search), `state` (filter), `with`
  (eager-load) are correct as-is.
- Changing default or max values. Each endpoint's defaults are tuned to
  its workload; this spec only renames the parameter.

## Implementation Plan

1. Extract `parsePageLimit(query url.Values, def, max int) (int, error)`
   helper, probably in `server/internal/handlers/base/`. Tests for the
   helper covering: empty (returns default), valid value, value > max
   (clamped), invalid (returns error), legacy `size` alias precedence.
2. Migrate `results/handler.go` to use the helper. Test passes.
3. Migrate the three sites in `events/handler.go`. Test passes.
4. Migrate `incidents/handler.go`. Test passes.
5. Update `docs/api-specification.md` and `CLAUDE.md`.
6. Spot-check `web/dash0/src/api/hooks.ts`, CLI, MCP — fix any inlined
   `size=` references.
7. `make test` + `make lint`.
8. Manual smoke.
9. Commit, archive spec, merge.
