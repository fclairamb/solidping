---
model: opus
effort: medium
pr: batch
automerge: false
---

# The `checkUid` list filter of results and incidents resolves every identifier with its own query, uncapped and with no dedup

## Problem

Found by the security scan of 2026-11-07 (finding AG1).

**Severity: medium.** Any org member can trigger it, the read-only `viewer` role included: both routes are plain `orgGroup` GETs. On SaaS, sign-up is open and the org creator is its owner, so anyone can.

`GET /api/v1/orgs/:org/results` (`server/internal/app/server.go:1570`) and `GET /api/v1/orgs/:org/incidents` (`server.go:1594`) accept `checkUid` as a comma-separated list of check UIDs or slugs:

- `server/internal/handlers/results/handler.go:39-41` splits it with no cap. `buildListFilter` passes the list to `resolveCheckIdentifiers` (`results/service.go:236-239`).
- `server/internal/handlers/incidents/handler.go:92-94` splits it the same way. `ListIncidents` passes it to its own `resolveCheckIdentifiers` (`incidents/service.go:2742-2751`).
- Both resolvers (`results/service.go:326-344`, `incidents/service.go:2818-2839`) loop over the list and run one sequential `GetCheckByUidOrSlug` per entry. They have no cap and no dedup. Lookup errors are ignored, so the loop keeps going even after the request context is canceled.
- The MCP `list_results` and `list_incidents` tools reach the same resolvers with the same comma-split (`server/internal/mcp/tools_results.go:209-211`, `tools_incidents.go:107`).

What bounds the list is only the request size. The `http.Server` sets no `MaxHeaderBytes` (`server.go:3762-3766`), so Go's default of 1 MiB applies, and the in-process TLS edge serves the same handler. One-character identifiers separated by commas give roughly half a million entries in one request line, so a single GET runs that many DB round trips back to back. Repeating one identifier is enough: nothing needs to exist.

- On Postgres, each such request holds a pool connection for minutes. A handful in parallel drains the pool, and every tenant on the instance gets errors or timeouts.
- On SQLite, the lookups contend with the rest of the instance's DB traffic for the same database.
- After the loop, the surviving UIDs go into one `check_uid IN (...)`. With duplicates kept, that statement grows with the input as well.

This is the read-side twin of AE1 (`specs/done/2026/11/2026-11-05-04-bulk-check-update-uid-cap.md`), which capped the bulk update's UID list before dedup.

## Proposal

1. Add a shared cap on the raw list, for example `MaxCheckFilterIdentifiers = 100`. A dashboard filter never needs more, and dash0 sends a handful at most. Enforce it on the raw split, before trimming or dedup, in:
   - `results/handler.go` `ListResults` and `incidents/handler.go` `parseListIncidentsOptions`: more than the cap is a 400 `VALIDATION_ERROR` naming `checkUid`.
   - The MCP tools: the same limit returns an `errorResult`.
   - The two `resolveCheckIdentifiers`: refuse or truncate, as a backstop for any other caller.
2. In both resolvers, trim and dedup through a `map[string]struct{}` while keeping the order, so each distinct identifier is looked up once.
3. In both resolvers, stop and return on `ctx.Err() != nil` instead of looping on canceled lookups.
4. Optional, if it stays small: resolve the distinct identifiers in one org-scoped query instead of N. Split them into UUID-shaped values and slugs, then run `uid IN (...) OR slug IN (...)` with `deleted_at IS NULL`, in both dialects. `GetChecksByUIDs` (`db/postgres/postgres.go:1831-1835`) shows the chunked pattern. The cap alone already fixes the DoS.
5. Document the cap on the `checkUid` parameter of both routes in `server/internal/app/openapi/openapi.yaml`.

## Tests

- `server/internal/handlers/results/handler_test.go` (or a new `list_check_filter_test.go`):
  - Regression that fails before the fix: `checkUid` with cap + 1 entries, all the same slug, is a 400 `VALIDATION_ERROR`. Today it is a 200.
  - Regression that fails before the fix: the resolver called with cap entries that repeat one real check's slug runs `GetCheckByUidOrSlug` once. Count calls with a counting `db.Service` wrapper or a query hook.
  - Positive control: two real checks, by UID and by slug, plus one unknown identifier, under the cap. Exactly the two checks' results come back, as today.
- `server/internal/handlers/incidents/list_by_slug_test.go`: the same three cases for `/incidents`. A list of only unknown identifiers still returns the explicit empty page (`incidents/service.go:2742-2749`).
- `server/internal/mcp`: `list_results` with cap + 1 identifiers returns an error result, and with two valid ones it returns their results.
