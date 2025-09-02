# Status Page: Availability & Performance Data

## Overview

Enrich the public status page with three new visual elements per check:
1. **Overall availability percentage** (e.g., "99.986%") displayed alongside the status badge
2. **Daily availability bar chart** — a row of colored bars (one per day, configurable 7/30/90 days) showing up/degraded/down/noData
3. **Response time graph** — an area chart showing p95 response time over the same period

Display options are configurable per status page: `showAvailability`, `showResponseTime`, `historyDays`.

## Motivation

The current public status page shows only the current status ("Operational" / "Outage") with no historical context. Compared to BetterStack, Atlassian Statuspage, or similar tools, it looks empty and provides no confidence to end-users about service reliability. Adding historical availability and performance data matches industry expectations for status pages.

## Current State

**Public status page** (`apps/status0/src/components/shared/status-page-view.tsx`):
- Renders sections → resources with: status dot, name, type badge, status badge
- No historical data, no availability %, no charts

**Public API** (`GET /api/v1/status-pages/:org/:slug`):
- Returns page metadata + sections + resources with live check status (`up`/`down`/`degraded`)
- Does not include any results or availability data

**Results system** (already exists):
- Aggregation job produces hourly/daily/monthly results with `availability_pct`, `duration_min/max/p95`, `total_checks`, `successful_checks`
- Results API (`GET /api/v1/orgs/:org/results`) supports filtering by `periodType`, `checkUid`, `periodStartAfter`, and includes availability fields via `with` parameter
- Results API requires authentication — not accessible from the public status page

---

## 1. Database: Add Display Configuration to `status_pages`

**Migration**: `20260322000001_status_page_display`

```sql
ALTER TABLE status_pages ADD COLUMN show_availability boolean NOT NULL DEFAULT true;
ALTER TABLE status_pages ADD COLUMN show_response_time boolean NOT NULL DEFAULT true;
ALTER TABLE status_pages ADD COLUMN history_days integer NOT NULL DEFAULT 90;
```

**Constraints**: `history_days` must be one of `7`, `30`, `90`. Validated in the service layer.

**Model** (`internal/db/models/status_page.go`):
```go
ShowAvailability bool `bun:"show_availability,notnull,default:true"`
ShowResponseTime bool `bun:"show_response_time,notnull,default:true"`
HistoryDays      int  `bun:"history_days,notnull,default:90"`
```

---

## 2. Backend: Enrich Public View with Availability Data

### New Response Types

```go
type ResourceAvailabilityData struct {
    OverallAvailabilityPct *float64                `json:"overallAvailabilityPct,omitempty"`
    DailyAvailability     []DailyAvailabilityPoint `json:"dailyAvailability,omitempty"`
    ResponseTimeData      []ResponseTimePoint      `json:"responseTimeData,omitempty"`
}

type DailyAvailabilityPoint struct {
    Date            string  `json:"date"`            // "2026-03-21"
    AvailabilityPct float64 `json:"availabilityPct"`
    Status          string  `json:"status"`          // "up" (>=99.9%), "degraded" (>=99%), "down" (<99%), "noData"
}

type ResponseTimePoint struct {
    Date        string   `json:"date"`
    DurationP95 *float32 `json:"durationP95,omitempty"`
}
```

### Enrichment in ViewStatusPage

After the existing `getCheckInfo()` enrichment, add a second pass:

1. Collect all `checkUIDs` from resources
2. Single `ListResults` query: `PeriodTypes: ["day"]`, `CheckUIDs: allCheckUIDs`, `PeriodStartAfter: now - historyDays`, `size: historyDays * len(checkUIDs)`
3. Group results by `CheckUID`
4. For each resource, build:
   - `DailyAvailability`: one entry per day, filling missing dates with `status: "noData"`
   - `OverallAvailabilityPct`: weighted average of daily availability (weighted by `totalChecks`)
   - `ResponseTimeData`: p95 duration per day (only if `showResponseTime`)
5. Attach to resource response as `availability` field

### API Response Changes

`StatusPageResponse` gains:
```json
{
  "showAvailability": true,
  "showResponseTime": true,
  "historyDays": 90
}
```

`StatusPageResourceResponse` gains:
```json
{
  "availability": {
    "overallAvailabilityPct": 99.986,
    "dailyAvailability": [
      {"date": "2025-12-23", "availabilityPct": 100, "status": "up"},
      {"date": "2025-12-24", "availabilityPct": 98.5, "status": "down"},
      ...
    ],
    "responseTimeData": [
      {"date": "2025-12-23", "durationP95": 145.2},
      ...
    ]
  }
}
```

### Management API

Extend create/update request types for status pages:
```json
{
  "showAvailability": true,
  "showResponseTime": false,
  "historyDays": 30
}
```

---

## 3. Frontend (status0): Availability Bar Component

**New file**: `apps/status0/src/components/shared/availability-bar.tsx`

Pure CSS approach — no charting library:

```
[Check Name]  [Type]                        [Status Badge]  99.986%
[||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||||]
 90 days ago                   99.986% uptime                 Today
```

Each bar is a thin `<div>` (2-3px wide, ~24px tall) with background color:
- Green (`#22c55e`): availability >= 99.9%
- Yellow (`#eab308`): availability >= 99%
- Red (`#ef4444`): availability < 99%
- Gray (`#d1d5db`): no data

Hover tooltip shows: date + exact availability % (e.g., "Mar 21 — 99.986%")

Layout: flexbox row with `gap: 1px`, bars fill available width.

---

## 4. Frontend (status0): Response Time Chart

**New file**: `apps/status0/src/components/shared/response-time-chart.tsx`

Add `recharts` as dependency (consistent with dash0).

Simple area chart:
- Line: p95 response time
- X-axis: dates (formatted as "Mar 21")
- Y-axis: response time in ms (auto-scaled)
- Tooltip: date + p95 value
- Height: ~120px
- Color: blue area fill with opacity

---

## 5. Frontend (status0): Updated Resource Display

**File**: `apps/status0/src/components/shared/status-page-view.tsx`

Each resource expands from a single row to a card-like display:

```
┌─────────────────────────────────────────────────────────┐
│ ● Check Name  [http]                    Operational     │
│                                          99.986%        │
│ [||||||||||  |||||||||||  |||||| |||||||||||||||||||||]  │
│  90 days ago              99.986% uptime         Today  │
│                                                         │
│  Response Time                                          │
│  ▁▂▃▂▁▂▄▃▂▁▂▃▂▁▂▃▂▁▂▃▂▁▂▃▂▁▂▃▂▁▂▃▂▁▂▃▂▁▂▃▂▁▂▃▂▁▂▃   │
└─────────────────────────────────────────────────────────┘
```

Components shown conditionally based on page config:
- Availability bars + percentage: when `showAvailability` is true
- Response time chart: when `showResponseTime` is true
- When both are false: display remains as current (status dot + badge only)

---

## 6. Frontend (dash0): Configuration UI

**File**: `apps/dash0/src/components/shared/status-page-form.tsx`

Add a "Display Options" section below the existing form fields:

```
Display Options
─────────────────────────
Show Availability       [toggle: on]
Show Response Time      [toggle: on]
History Period          [select: 7 days / 30 days / 90 days]
```

---

## Testing Strategy

### Backend

- **Unit tests** (`internal/handlers/statuspages/service_test.go`):
  - `getResourceAvailability` returns correct daily points with gap-filling
  - Weighted overall availability computation is correct
  - `ViewStatusPage` includes availability data when configured
  - `ViewStatusPage` omits availability data when `showAvailability=false` and `showResponseTime=false`
  - Create/update status page with display config fields

- **Migration tests**: Verify migration applies and defaults are correct

### Frontend E2E

- Create a status page with display options via dash0
- Visit public URL, verify:
  - Availability bars render (correct number of bars = historyDays)
  - Overall availability % is displayed
  - Response time chart renders
  - Toggling `showAvailability` off hides bars and percentage
  - Toggling `showResponseTime` off hides chart

### Manual

```bash
make dev-test
# Create checks, wait for some results to accumulate
# Create a status page with sections and resources
# Visit /status-pages/test/<slug>
# Verify visual output matches expected layout
```

---

## Implementation Steps

1. Migration files (SQLite + Postgres) + model changes
2. Service: response types + `getResourceAvailability` + enrichment in `ViewStatusPage`
3. Handler: create/update request types for display config
4. status0: types + availability bar component
5. status0: response time chart component (+ add recharts dependency)
6. status0: integrate components into `status-page-view.tsx`
7. dash0: display options in status page form
8. Tests

---

**Status**: Draft | **Created**: 2026-03-22
