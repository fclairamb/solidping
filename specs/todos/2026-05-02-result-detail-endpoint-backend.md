# Result detail endpoint — backend

## Context

The dash0 check detail page (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`) shows a "Recent results" table with rows that are not clickable. There is no way to deep-link to one specific result and inspect everything about it (status, region, worker, duration, full metrics blob, full output blob, period boundaries for aggregations).

The backend has a list endpoint (`GET /api/v1/orgs/:org/results`, in `server/internal/handlers/results/handler.go:32`) but **no single-result fetch endpoint**. The Result model lives at `server/internal/db/models/result.go:61` with these fields relevant to the detail view:

- `UID` (UUIDv7), `OrganizationUID`, `CheckUID`, `Region`, `WorkerUID`
- `PeriodType` (`raw` | `hour` | `day` | `month`), `PeriodStart`, `PeriodEnd`
- `Status`, `Duration`, `Metrics`, `Output` (raw rows)
- `TotalChecks`, `SuccessfulChecks`, `AvailabilityPct`, `DurationMin`, `DurationMax`, `DurationP95` (aggregated rows)

An aggregation job (`server/internal/jobs/jobtypes/job_aggregation.go:34`) periodically rolls `raw → hour → day → month` and **deletes the source rows** once they're rolled up. So a UID written into a URL today is not guaranteed to exist tomorrow: a raw result older than the configurable raw-retention window (~24h, see `2026-05-02-configurable-aggregation-retention.md`) becomes part of the hourly aggregation for the same check + region, and the raw row's UID is gone.

Bookmarked or shared result URLs would die silently. We need a "find the aggregation that covers this UID" fallback so links survive rollups, while still telling the viewer that the original raw result is no longer available.

## Scope

In scope:
- New endpoint `GET /api/v1/orgs/:org/checks/:check/results/:uid` returning a single result, with fallback to the smallest-period aggregation that covers the requested UID's timestamp when the UID itself is gone.
- Service layer logic, including UUIDv7 timestamp extraction.
- Handler tests (HTTP status mapping) and service tests (lookup, fallback selection, edge cases).
- One line in `docs/api-specification.md`.

Out of scope:
- The frontend page that consumes this — see `2026-05-02-result-detail-page-frontend.md`.
- A flat `GET /api/v1/orgs/:org/results/:uid` endpoint without check scoping. The nested form is enough for dash0 and avoids a `WHERE uid = ?` scan across an org's whole results table.
- Modifying the existing list endpoint.

## Endpoint shape

`GET /api/v1/orgs/:org/checks/:check/results/:uid`

- `:org` — org slug, resolved by existing org middleware.
- `:check` — check UID or slug, resolved via the existing pattern (`Service.resolveCheckIdentifiers` in `server/internal/handlers/results/service.go:173` already does this for the list endpoint; reuse).
- `:uid` — the result UID (UUIDv7) the caller has.

Auth: same middleware as the rest of `/api/v1/orgs/:org/...`.

### Response 200 — exact match found

JSON shape is the same as one row from the existing list endpoint (i.e. `OrgResult` in `web/dash0/src/api/hooks.ts:101`). No `fallback` field.

### Response 200 — fallback to a covering aggregation

Same shape, plus a top-level `fallback` object:

```json
{
  "uid": "0196abcd-...-aggregated-uid",
  "checkUid": "...",
  "periodType": "hour",
  "periodStart": "2026-05-01T14:00:00Z",
  "periodEnd":   "2026-05-01T15:00:00Z",
  "totalChecks": 60, "successfulChecks": 60, "availabilityPct": 100,
  "...": "...",
  "fallback": {
    "requestedUid": "019de972-7ef4-7d49-b84d-9f9523dd8ab3",
    "requestedAt":  "2026-05-01T14:23:18.452Z",
    "reason":       "rolled_up_to_hour"
  }
}
```

`reason` is one of `rolled_up_to_hour`, `rolled_up_to_day`, `rolled_up_to_month` — encoding which aggregation level we landed on. The client uses this to render the banner.

### Response 404

Two situations:
- The requested UID is not parseable as UUIDv7 (we can't extract a timestamp, so we can't fall back).
- The UID is parseable but no covering aggregation row exists for the same check (e.g. month aggregation has also been deleted, or the UID was never valid).

Body: standard error envelope, `code: "RESULT_NOT_FOUND"`. Add `ErrorCodeResultNotFound` to `server/internal/handlers/base/errors.go` if not already present (grep first; error codes are defined centrally per CLAUDE.md).

### Response 403 / 401

Standard middleware. The check is resolved against the org, so a request for a check that doesn't belong to the org returns 404 (CHECK_NOT_FOUND), same as the existing pattern in `checks` handlers.

## Service algorithm

In `server/internal/handlers/results/service.go`, add `GetResult(ctx, orgUID, checkUID, resultUID string) (*Result, *FallbackInfo, error)`.

```
1. Resolve check: existing resolveCheckIdentifiers([resultUID's check param]).
   Bail with ErrCheckNotFound if it doesn't belong to the org.

2. Try direct lookup:
     SELECT * FROM results
     WHERE organization_uid = $org AND check_uid = $check AND uid = $uid
     LIMIT 1
   If found → return (row, nil, nil).

3. Parse the UID as UUIDv7. If parse fails or version != 7 → return ErrResultNotFound.
   Extract the embedded millisecond timestamp:
     ms := int64(uuidBytes[0])<<40 | ... | int64(uuidBytes[5])
     ts := time.UnixMilli(ms).UTC()
   (Use `github.com/google/uuid` — already in go.mod since `result.go:96` calls uuid.NewV7().
   `u.Time()` returns a v6/v7 timestamp; use that rather than rolling our own.)

4. Look up the smallest-period aggregation that covers ts, in priority order:
     for _, level := range []string{"hour", "day", "month"} {
       SELECT * FROM results
       WHERE organization_uid = $org
         AND check_uid       = $check
         AND period_type     = $level
         AND period_start   <= $ts
         AND (period_end IS NULL OR period_end > $ts)
       ORDER BY period_start DESC
       LIMIT 1
     }
   First hit wins. If a level returns multiple rows (one per region — aggregations
   are per-region), pick the one with the same region as the original UID would
   have had IF we can derive it; otherwise pick the highest-coverage one
   (most total_checks). See "Region disambiguation" below.

5. If a hit is found → return (row, &FallbackInfo{
     RequestedUID: uid, RequestedAt: ts, Reason: "rolled_up_to_" + level
   }, nil).
   Otherwise → ErrResultNotFound.
```

### Region disambiguation

A check that runs in N regions produces N aggregated rows per period, one per region. Without the original raw row we don't know which region the requested UID was for. Two acceptable behaviours:

- **(a)** Pick the row with the highest `total_checks` (most signal). Simple and stable.
- **(b)** Pick the first by `region ASC` for determinism.

Go with **(a)**. It's what a human would mean by "the most likely covering aggregation." Add a tie-break by region ASC for determinism when counts are equal.

If the user later wants region-aware fallback, an optional `?region=` query param can be added. Don't ship that yet.

### UUIDv7 caveat — what timestamp does the UID carry?

UUIDv7s are generated at insertion time. For raw results that's `~PeriodStart` (within milliseconds — `result.go:96` runs in the same flow that sets `PeriodStart`). For aggregated results that's the aggregation job's run time, which is shortly after the bucket closed (e.g. an hourly bucket [14:00, 15:00) gets a row with UUIDv7 timestamp ~15:00:NN). That's fine for the fallback math: 15:00:NN falls inside the daily bucket [00:00, 24:00), and inside the monthly bucket. So the algorithm works whether the requested UID was raw or already-aggregated-but-since-rolled-up.

Document this with a one-line code comment so the next reader doesn't worry about it.

## Files to touch

- `server/internal/handlers/results/handler.go` — add `GetResult` handler (~50 LOC), register route in `server/internal/app/server.go` next to the existing `results` route registration.
- `server/internal/handlers/results/service.go` — add `GetResult`, `parseUUIDv7Timestamp` helper (or use `uuid.UUID.Time()`), `FallbackInfo` struct.
- `server/internal/handlers/base/errors.go` — add `ErrorCodeResultNotFound = "RESULT_NOT_FOUND"` if not present.
- `server/internal/handlers/results/handler_test.go` — add cases (see below).
- `server/internal/handlers/results/service_test.go` — add cases (see below).
- `docs/api-specification.md` — add one stanza under the Results section.
- `CLAUDE.md` — append `RESULT_NOT_FOUND` to the standard error codes list.

## Tests

`handler_test.go`:
- `TestGetResult_DirectHit` — seed a raw result, fetch by uid, expect 200 + matching uid, no `fallback` field.
- `TestGetResult_FallbackToHour` — seed an hourly aggregation covering 14:00–15:00, request a fabricated UUIDv7 with embedded timestamp 14:23, expect 200 + `fallback.reason == "rolled_up_to_hour"`.
- `TestGetResult_FallbackToDay` — only a day aggregation exists; request a UUIDv7 inside the day; expect `rolled_up_to_day`.
- `TestGetResult_NotFoundNoCoverage` — UID parseable but no aggregation covers ts → 404 `RESULT_NOT_FOUND`.
- `TestGetResult_NotFoundBadUID` — request `uid=not-a-uuid` and `uid=00000000-0000-1000-8000-000000000000` (v1) → 404.
- `TestGetResult_WrongOrg` — request a result that exists but in another org → 404 (via check-not-found path).
- `TestGetResult_Unauthenticated` → 401.

`service_test.go`:
- `TestParseUUIDv7Timestamp` — round-trip: generate a v7 with `uuid.NewV7()`, extract ts, assert within 1ms of `time.Now()`.
- `TestParseUUIDv7Timestamp_RejectsV4` — returns error.
- `TestPickCoveringAggregation_ChoosesHourOverDay` — seed both, hour wins.
- `TestPickCoveringAggregation_RegionTieBreak` — two hour-rows for same period, different regions, pick the one with more `total_checks`; if equal, alphabetical region.
- `TestPickCoveringAggregation_PeriodEndExclusive` — a request with ts == period_end should NOT match that row (boundary condition: aggregations are `[start, end)`).

Use testcontainers per `server/CLAUDE.md`. Use `testify/require` and `t.Parallel()`.

## Verification

1. `make lint-back` clean.
2. `make test` passes (the new tests + no regressions).
3. Manual smoke against `make dev-test`:
   ```bash
   TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
     -d '{"org":"test","email":"test@test.com","password":"test"}' \
     http://localhost:4000/api/v1/auth/login | jq -r '.accessToken')

   # pick a recent raw result uid from the list endpoint
   UID=$(curl -s -H "Authorization: Bearer $TOKEN" \
     'http://localhost:4000/api/v1/orgs/test/results?size=1' | jq -r '.data[0].uid')
   CHECK=$(curl -s -H "Authorization: Bearer $TOKEN" \
     'http://localhost:4000/api/v1/orgs/test/results?size=1' | jq -r '.data[0].checkUid')

   # direct hit
   curl -s -H "Authorization: Bearer $TOKEN" \
     "http://localhost:4000/api/v1/orgs/test/checks/$CHECK/results/$UID" | jq '.'

   # forge a fake-but-plausible v7 inside an existing hour bucket (use a real hourly row's periodStart + 23 minutes, encode as v7 manually) and confirm fallback fires
   ```
4. `make build` (full build with embedded dash0) still green.

## Final grep check

After landing, this should return zero matches except the new code:

```bash
rtk grep -rn "GetResult\b" server/internal/handlers/results/
```

(should show: handler.go, service.go, handler_test.go, service_test.go, server.go route registration)

---

## Implementation Plan

1. Add `ErrorCodeResultNotFound = "RESULT_NOT_FOUND"` in `server/internal/handlers/base/errors.go` (only if not already present).
2. In `server/internal/handlers/results/service.go` add a `FallbackInfo` struct + `GetResult(ctx, orgUID, checkID, resultUID)` method: direct lookup → on miss, parse UID as v7, extract timestamp via `uuid.UUID.Time().UnixTime()`, then walk hour/day/month and return the first covering aggregation; on multiple hits, pick highest `total_checks` with `region ASC` tie-break.
3. In `server/internal/handlers/results/handler.go` add a `GetResult` HTTP handler that returns the result + optional `fallback` field; map the service errors (`check not found` → 404 CHECK_NOT_FOUND, `result not found` → 404 RESULT_NOT_FOUND).
4. Register `GET /api/v1/orgs/:org/checks/:check/results/:uid` next to the existing results routes in `server/internal/app/server.go`.
5. Service tests (use the existing testcontainers harness): direct-hit, fallback-to-hour, fallback-to-day, no-coverage→404, bad UID→404, region tie-break.
6. Update `docs/api-specification.md` and `CLAUDE.md` (error codes list).
