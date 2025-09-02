# Check Detail Dashboard (BetterStack-inspired)

## Overview

Improve the check detail page with summary cards, a response time chart, and an availability table — similar to BetterStack's monitor detail view. This gives users immediate visibility into uptime, response times, and incident history without needing to scan raw result rows.

## Motivation

1. The current check detail page only shows raw configuration and a 10-row recent results table — no visual trends or summary statistics.
2. Users need to quickly assess: "Is this check healthy? How has it performed over time? What's the availability?"
3. Recharts (v2.15.4) is already installed but unused — this is a natural first use.

## Current State

**Check detail page** (`apps/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`):
- Header with status dot, name, slug editor, type badge, edit/refresh/delete buttons
- Configuration card (type, period, config JSON, labels)
- Last Result card (timestamp, metrics, output)
- Recent Results table (10 rows: time, status, duration, region)
- Recent Incidents table (5 rows: started, state, duration, view link)

**Backend data already available**:
- Raw results with `duration_ms` metric
- Aggregated results (hour/day/month/year) with `availability_pct`, `duration_min`, `duration_max`, `duration_p95`
- `PeriodStartAfter`/`PeriodEndBefore` filtering at DB layer (`back/internal/db/models/result.go:105-107`) but NOT exposed in the HTTP handler
- Incidents with `startedAt`, `resolvedAt`, `state`; `Since`/`Until` filtering at service layer (`back/internal/handlers/incidents/service.go:308-309`) but NOT parsed from query params

**Note**: The HTTP checker only stores total `duration_ms` — no DNS/connection/TLS/transfer breakdown. We chart total response time only.

---

## Backend Changes

### 1. Results handler — add time range params

**File**: `back/internal/handlers/results/handler.go`

Parse two new query parameters after the existing `periodType` block (~line 62):

```go
// periodStartAfter - RFC3339 timestamp
if v := query.Get("periodStartAfter"); v != "" {
    t, err := time.Parse(time.RFC3339, v)
    if err != nil {
        return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid periodStartAfter: must be RFC3339")
    }
    opts.PeriodStartAfter = &t
}

// periodEndBefore - RFC3339 timestamp
if v := query.Get("periodEndBefore"); v != "" {
    t, err := time.Parse(time.RFC3339, v)
    if err != nil {
        return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid periodEndBefore: must be RFC3339")
    }
    opts.PeriodEndBefore = &t
}
```

**File**: `back/internal/handlers/results/service.go`

Add to `ListResultsOptions`:
```go
PeriodStartAfter *time.Time
PeriodEndBefore  *time.Time
```

Pass to DB filter in `ListResults`:
```go
filter.PeriodStartAfter = opts.PeriodStartAfter
filter.PeriodEndBefore = opts.PeriodEndBefore
```

### 2. Incidents handler — add time range params

**File**: `back/internal/handlers/incidents/handler.go`

Parse `since` and `until` after the existing `state` block (~line 51):

```go
if v := query.Get("since"); v != "" {
    t, err := time.Parse(time.RFC3339, v)
    if err != nil {
        return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid since: must be RFC3339")
    }
    opts.Since = &t
}

if v := query.Get("until"); v != "" {
    t, err := time.Parse(time.RFC3339, v)
    if err != nil {
        return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid until: must be RFC3339")
    }
    opts.Until = &t
}
```

---

## Frontend Changes

### 3. API hooks (`apps/dash0/src/api/hooks.ts`)

**Extend `useResults` options**:
```typescript
periodStartAfter?: string;
periodEndBefore?: string;
```
Add to query params:
```typescript
if (options?.periodStartAfter) params.set("periodStartAfter", options.periodStartAfter);
if (options?.periodEndBefore) params.set("periodEndBefore", options.periodEndBefore);
```

**Extend `useIncidents` options**:
```typescript
since?: string;
until?: string;
```
Add to query params:
```typescript
if (options?.since) params.set("since", options.since);
if (options?.until) params.set("until", options.until);
```

**Extend `OrgResult` type**:
```typescript
durationMinMs?: number;
durationMaxMs?: number;
```

### 4. Summary cards (`apps/dash0/src/components/checks/check-summary-cards.tsx`) — new file

Three cards in a `grid grid-cols-1 md:grid-cols-3 gap-4` row:

| Card | Source | Display |
|------|--------|---------|
| Currently up/down for | `check.lastStatusChange.time` + `check.lastResult.status` | Live-updating duration (green if up, red if down) |
| Last checked | `check.lastResult.timestamp` | Relative time via `date-fns/formatDistanceToNow` |
| Incidents | `incidents.total` or `incidents.data.length` | Count with icon |

Uses existing `Card`, `CardContent` components. Live timer follows the `IncidentDuration` pattern already in the check detail page (useState + setInterval).

### 5. Response time chart (`apps/dash0/src/components/checks/response-time-chart.tsx`) — new file

**Controls**: Day / Week / Month button group + optional region selector (only if 2+ regions)

**Data fetching per time range**:

| Range | periodType | periodStartAfter | size | with |
|-------|-----------|-----------------|------|------|
| Day | raw | now - 24h | 100 | durationMs,region |
| Week | hour | now - 7d | 100 | durationMs,durationMinMs,durationMaxMs,region |
| Month | day | now - 30d | 100 | durationMs,durationMinMs,durationMaxMs,region |

**Chart**: Recharts `AreaChart` inside `ResponsiveContainer` (height 300px):
- Data reversed from API's DESC to ASC
- `XAxis`: formatted timestamps (HH:mm for Day, "Mon"/"Tue" for Week, "Feb 1" for Month)
- `YAxis`: milliseconds
- Single `Area` for total response time with gradient fill
- `Tooltip` showing exact value and timestamp
- Region filtering applied client-side

Wrapped in a `Card` with title "Response Times".

### 6. Availability table (`apps/dash0/src/components/checks/availability-table.tsx`) — new file

**Rows**: Today, Last 7 days, Last 30 days, Last 365 days

**Columns**: Availability %, Downtime, Incidents, Longest incident, Avg. incident

**Data fetching**:

| Period | periodType | periodStartAfter | with |
|--------|-----------|-----------------|------|
| Today | hour | start of today | availabilityPct |
| 7 days | day | now - 7d | availabilityPct |
| 30 days | day | now - 30d | availabilityPct |
| 365 days | month | now - 365d | availabilityPct |

Availability % = weighted average of `availabilityPct` from aggregated results.
Downtime = `(1 - availability/100) * periodDuration`, formatted as duration.

Incident stats from `useIncidents(org, { checkUid, size: 100 })`, filtered client-side per time window:
- Count: incidents with `startedAt` in window
- Longest: `max(resolvedAt - startedAt)` (use `now` for active incidents)
- Average: `sum(durations) / count`

Uses existing `Table` components. Wrapped in a `Card` with title "Availability".

### 7. Restructure check detail page

**File**: `apps/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`

New layout order:
1. Header (existing, unchanged)
2. **Summary cards** (new — `<CheckSummaryCards>`)
3. **Response time chart** (new — `<ResponseTimeChart>`)
4. **Availability table** (new — `<AvailabilityTable>`)
5. Configuration + Last Result cards (existing, moved below)
6. Recent Results table (existing)
7. Recent Incidents table (existing)

Change incidents query `size` from 5 to 100 for availability calculations.

---

## Testing Strategy

### Manual
1. `make test` — backend tests pass
2. `make lint` — no lint errors
3. Start dev servers, navigate to a check detail page
4. Verify summary cards show correct uptime duration / last checked / incident count
5. Toggle Day/Week/Month on chart — verify data changes and chart re-renders
6. Verify availability table shows percentages and incident stats per time period
7. Verify existing functionality (edit, delete, slug editing, recent results/incidents tables) still works

### API
```bash
# Test new time range params
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d '{"org":"default","email":"admin@solidping.com","password":"solidpass"}' \
  'http://localhost:4000/api/v1/auth/login' | jq -r '.accessToken')

# Results with time range
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/results?periodType=hour&periodStartAfter=2026-02-15T00:00:00Z&with=availabilityPct,durationMs'

# Incidents with time range
curl -s -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4000/api/v1/orgs/default/incidents?since=2026-02-15T00:00:00Z'
```

## Implementation Steps

1. Add `periodStartAfter`/`periodEndBefore` to results handler + service
2. Add `since`/`until` to incidents handler
3. Extend frontend hooks and types in `hooks.ts`
4. Create `check-summary-cards.tsx`
5. Create `response-time-chart.tsx`
6. Create `availability-table.tsx`
7. Restructure check detail page to integrate new components
8. Verify build, lint, and manual testing

---

**Status**: Draft | **Created**: 2026-02-16
