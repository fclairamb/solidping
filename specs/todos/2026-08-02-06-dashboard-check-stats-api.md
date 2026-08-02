---
model: opus
effort: high
---

# Dashboard check stats are wrong past 100 checks — add a cached stats API (GitHub issue #172)

## Problem

[Issue #172](https://github.com/fclairamb/solidping/issues/172): with 250+ checks the
dashboard counters are wrong. They should come from a statistics API with ~1-5 minutes
of caching instead of being derived client-side.

Root cause: the org dashboard computes every KPI client-side from a **single page**
of the checks list:

- `web/dash0/src/components/dashboard/dashboard-page.tsx:222-225` fetches
  `useChecks(org, { with: "last_result,last_status_change", limit: 1000 })`;
  `useChecks` (`web/dash0/src/api/hooks.ts:305-326`) is a plain `useQuery` that
  ignores `response.pagination.cursor` — it never paginates.
- The backend silently clamps `limit` to 100
  (`server/internal/handlers/checks/handler.go:175-177`), so at most 100 checks come
  back and `enabledCount` / `disabledCount` / `downCount` / `hardDownCount`
  (`dashboard-page.tsx:282-289`) plus `checksCount` / `totalCount` (:369, :447) are
  all wrong for orgs with more than 100 checks.
- No stats/summary endpoint exists for checks today. The closest precedent is the
  check-group status roll-up `GetCheckGroupStatusCounts`
  (`server/internal/handlers/checkgroups/service.go:140`, exposed as
  `memberStatusCounts`).

## Proposal

1. **New endpoint** `GET /api/v1/orgs/:org/checks/stats` (checks handler package)
   returning an aggregate object, e.g.:

   ```json
   {
     "total": 262,
     "enabled": 250,
     "disabled": 12,
     "byStatus": { "up": 240, "down": 6, "degraded": 2, "paused": 12, "pending": 2 },
     "down": 6,
     "hardDown": 3
   }
   ```

   Shape should cover everything `dashboard-page.tsx` currently derives:
   enabled/disabled, down, hard-down, and total. Follow REST conventions (camelCase,
   object response — it's not a list). Register it in the `orgChecks` route group in
   `server/internal/app/server.go` **before** the `/:checkUid` route so `stats`
   isn't captured as a UID.
2. **Server-side aggregation, not load-all-and-count**: implement as a SQL
   `GROUP BY` over the checks table (works on both PostgreSQL and SQLite — keep the
   two backends in sync). The status semantics must match what the frontend derives
   via `effectiveStatus` / `isDownStatus` / `isHardDownStatus` (maintenance, paused,
   pending nuances) — audit those helpers and mirror the mapping server-side; this
   is the correctness-sensitive part of the spec.
3. **Caching**: per-org in-memory cache with a short TTL (1 minute is enough — the
   issue allows 1-5). Keep it simple: a `sync.Map`/mutex-guarded map of
   `orgUID → {stats, computedAt}` inside the checks service. Document the TTL in the
   handler; no invalidation machinery needed at this staleness budget.
4. **Frontend**: add a `useCheckStats(org)` hook; make the dashboard KPI cards
   (`dashboard-page.tsx:365-428`) and total counters read from it. The dashboard may
   still fetch a page of checks for the recent/down listings it renders, but no
   counter may be derived from a paginated list anymore.
5. **Tests**: backend table-driven tests for the aggregation (mixed statuses,
   enabled/disabled, >100 checks proving the counters exceed the page clamp) on both
   DB backends, cache TTL behavior, and a dash0 update if the Playwright suite
   asserts the dashboard counters.
6. Update `wiki/api-specification/` and the OpenAPI spec
   (`server/internal/app/openapi/openapi.yaml`) with the new endpoint.

Open question for the implementer: whether `byStatus` should use the raw stored
status or the effective status (maintenance windows overriding). The dashboard
tiles need the *effective* view; if that's expensive to compute in SQL, computing
raw counts in SQL and applying the maintenance overlay in Go before caching is
acceptable — correctness over purity.
