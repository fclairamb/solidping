# Status page response-time chart: visualize downtime

## Context

The public status page (status0) shows a "Temps de réponse" / "Response time" line chart per check. The chart's data source `ResponseTimePoint` (`web/status0/src/api/hooks.ts:16-19`) carries only `time` and `durationP95`:

```ts
type ResponseTimePoint = { time: string; durationP95: number | null };
```

When a check is down/error/timeout, `Duration` is null on the backend (`server/internal/handlers/statuspages/service.go:918-944`), the frontend renders the gap as nothing, and a visitor sees a perfectly smooth blue line — no visual indication that the service was *down* during that window. The screenshot the user shared shows exactly this: the line continues uninterrupted across what may have been an outage.

The grey 90-day availability bar above the chart does indicate downtime (per-day aggregate), but visitors looking at "today" see no down state on the chart itself.

## Scope

**In scope:**
- Extend the wire format `ResponseTimePoint` with a `status` field.
- Backend (`statuspages/service.go:918-944`) populates it from `Result.Status`.
- status0 chart renders down/error/timeout points with a distinct visual: a thin red strip *below* the chart (one cell per data point) coloured by status. This pattern is compact, accessible, and used by Pingdom / UptimeRobot.
- Tooltip on hover shows the status label localized.
- Translations follow the existing i18n pattern (cf. `2026-05-02-09-status0-translate-availability-bar-and-chart.md`).

**Out of scope:**
- Per-region breakdown.
- Latency percentile bands (P50/P95/P99 stacked).
- Changing the response-time line for non-up samples (still null/gap is correct — there's no meaningful response time when the check failed).
- Showing this on the dash0 internal check detail page (separate; it has its own chart).

## Approach

### 1. Wire format

Extend the DTO and serializer:

`server/internal/handlers/statuspages/service.go:918-944` — when building each point, include the result status:

```go
points = append(points, ResponseTimePoint{
    Time:        r.PeriodStart,
    DurationP95: durationP95Ptr,
    Status:      string(r.Status), // "up" | "down" | "error" | "timeout"
})
```

`web/status0/src/api/hooks.ts:16-19`:
```ts
type ResponseTimePoint = {
  time: string;
  durationP95: number | null;
  status: "up" | "down" | "error" | "timeout";
};
```

### 2. Frontend rendering

`web/status0/src/components/shared/response-time-chart.tsx`:

Render the existing line chart unchanged (still uses only `durationP95`).

Below the chart's x-axis, add a 6–8px tall horizontal strip — one rect per data point, full-width. Colour by status:
- `up` → transparent / light gray (no drawing) so the "good" state is visually quiet.
- `down` → red (`var(--destructive)` or a fixed hex).
- `error` → orange.
- `timeout` → yellow.

Implementation: depending on the chart library, either:
- Use a second sub-axis / band area below the main chart.
- Render a sibling `<svg>` of the same width sharing the x-scale (simplest).

If the chart uses `recharts`, a `<ReferenceArea>` per non-up sample is workable but verbose. The sibling-svg approach is more flexible.

Tooltip: extend the existing tooltip to read `status`. When non-up, append the localized status label.

### 3. i18n

Add keys to `web/status0/src/locales/{fr,en}.json`:
- `status.up` → "Up" / "Opérationnel"
- `status.down` → "Down" / "Hors-service"
- `status.error` → "Error" / "Erreur"
- `status.timeout` → "Timeout" / "Délai dépassé"

(Reuse keys if they already exist for the availability bar.)

### 4. Tests

Backend (`server/internal/handlers/statuspages/service_test.go`):
- Build a series of mixed up/down results, call the response-time aggregator, assert each point's `Status` matches.

Frontend (Playwright in `web/status0/e2e/` if present, or a unit test):
- Mock the API to return a series with one `down` point.
- Assert a red rect is rendered at the corresponding x-position.

## Verification

1. `make test` passes new backend test.
2. `make dev`, on a check with at least one recent down result, visit the status0 page and confirm the strip below the chart shows red for that interval.
3. Hover the red strip — tooltip says "Down" (or "Hors-service" in fr).
4. Translation toggle works.
