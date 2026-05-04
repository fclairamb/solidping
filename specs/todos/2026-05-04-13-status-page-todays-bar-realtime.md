# Status page: today's daily availability bar updates in real-time

## Context

The 90-day grey bar strip on the public status page is fed by `dailyAvailability` aggregates produced by `server/internal/handlers/statuspages/service.go:731-739`, which queries with `PeriodTypes: ["day"]`. The synthesis path at `service.go:752-825` adds today's bar from raw/hourly results **only when no daily row already exists for today**.

Once the daily aggregator job creates today's row (typically shortly after midnight on the timezone boundary, or on a fixed cron), the synthesis branch is skipped on every subsequent request. The frontend polls every 30s (`web/status0/src/api/hooks.ts:67`) but the backend keeps returning the same stale value. New raw/hourly results don't update today's bar until tomorrow.

User impact: a check goes down at 09:00, the bar for today still shows green at 11:00, because the day aggregate was computed at 00:05 and hasn't been refreshed.

## Scope

**In scope:**
- For the **current calendar day only**: always synthesize the daily availability from live hourly (or raw) results on every request, regardless of whether a stored daily row exists. The stored row stays — but for today, it's overridden by a fresh re-aggregation.
- Past days (1–89 days back): keep using the stored daily aggregate.
- A short server-side cache (5–30s TTL) keyed by `(orgUID, checkUID, today)` if the synthesis cost is non-trivial. Default: skip the cache initially and add only if benchmarks demand it.
- Tests covering: today's bar reflects new hourly results without running the daily aggregator; past days are unchanged.

**Out of scope:**
- Per-hour bars on the public page (different feature).
- Changing the daily aggregator's schedule or behaviour.
- Frontend changes — the 30s poll already exists.

## Approach

### 1. Service logic

`server/internal/handlers/statuspages/service.go:752-825` — locate the conditional `if no daily row exists for today { synthesize }` and unconditionally run the synthesis for today.

Pseudocode:

```go
todayUTC := startOfDay(s.now(), check.timezone)

// Past days: stored daily rows.
pastRows := s.db.ListResults(ctx, ListResultsFilter{
    CheckUID:    check.UID,
    PeriodTypes: []string{"day"},
    PeriodStart: gte(todayUTC.AddDate(0, 0, -89)),
    PeriodEnd:   lt(todayUTC),
})

// Today: always synthesize fresh.
todayRow := s.synthesizeTodayAvailability(ctx, check, todayUTC)

return append(pastRows, todayRow)
```

`synthesizeTodayAvailability` aggregates hourly (or raw) results between `todayUTC` and `now`.

If the existing synthesis function already does this — extract it — reuse it; just remove the "skip if stored row exists" guard for the today case.

### 2. Beware of the timezone boundary

If a status page is configured with an organization or check timezone other than UTC, "today" means the local day. Confirm `startOfDay` honours the right zone. Re-using whatever the daily aggregator uses is the safest choice (consistency with stored rows on the same day).

### 3. Optional cache

If a profile shows the live aggregation taking >10ms, add a tiny in-process cache:

```go
type todayCache struct {
    sync.Map // key: orgUID+checkUID+todayUTC, value: cachedRow{at time.Time; row Result}
}

const todayCacheTTL = 15 * time.Second
```

Skip on first implementation; revisit only if needed.

### 4. Tests

`server/internal/handlers/statuspages/service_test.go`:

```go
func TestStatusPage_TodaysBar_ReflectsLiveResults(t *testing.T) {
    // Insert daily aggregate for "today" with availability=100% (stale).
    // Insert hourly results for today: 03:00 up, 09:00 down (1h outage).
    // Call the status page endpoint.
    // Assert today's daily bar shows availability < 100%.
}

func TestStatusPage_PastDays_UseStoredAggregate(t *testing.T) {
    // Insert stored daily for 7 days ago = 80%, hourly mismatched 95%.
    // Endpoint returns the 80% (stored), proving past days are not re-synthesized.
}
```

### 5. Manual smoke

1. Pick a healthy check.
2. Manually insert a `down` result via SQL or the worker for "now".
3. Reload the status page — today's bar shifts from full green to partly red within one poll cycle.

## Verification

1. `make test` passes the two new tests.
2. Manually: cause a brief outage on a status-page-listed check, refresh the public page within a minute, observe today's bar updates.
3. `select * from results where period_type='day' and period_start='today'` shows the (now-stale) stored row is unchanged — it's the rendering that's live, not the aggregate.
4. Past days are still the cheap stored aggregates (no perf regression on the 89 historical bars).
