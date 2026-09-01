---
model: opus
effort: high
---

# Uptime report email: period-over-period trends, latency summary, worst-first check list, degenerate-case guards

## Problem

The scheduled uptime report email (`server/internal/uptimereport/report.go`, rendered by
`server/internal/email/templates/uptime-report.html`) is availability- and incident-centric
and static: it shows one period in isolation. Compared with competitor monthly reports it
is missing the content that makes such a digest engaging and actionable:

1. **No trend.** There is no comparison with the previous period — "uptime vs last
   month", "incidents vs last month", "response time ±X%" are the numbers that make a
   recurring email worth opening, and we compute none of them, even though
   `slo.PreviousWindow` (used at `report.go:145`) already does the calendar math for the
   *current* window.
2. **No latency content at all.** `Data` (`report.go:94`) and `CheckRow` (`report.go:57`)
   carry only availability and incident fields, yet the inputs are nearly free:
   `uptimebar.BucketStats` already accumulates `DurCnt`/`DurSum` and exposes
   `AvgDuration()` (`server/internal/uptimebar/bucketing.go:108`), and aggregated result
   rows persist `duration_min` / `duration_max` / `duration_p95`.
3. **Incident aggregates are partial.** `availability.PeriodIncidents`
   (`server/internal/handlers/availability/service.go:94`) already yields `Count`,
   `LongestSeconds` and `TotalDowntimeSeconds`, and the report surfaces the first two —
   but not the derived **average incident duration**, which is often more telling than
   the longest.
4. **The check table is sorted alphabetically** (`report.go:215`), so a broken check can
   sit invisibly at the bottom of a 50-row table (`maxCheckRows`, `report.go:35`). And a
   single percentage per check hides *when* the trouble happened — a per-day color strip
   (as on status pages) makes it jump out.

Competitor reports also demonstrate the failure modes to avoid: a monitor down 100% of
the month still got a **green** "0.00% vs prev. month", a green "0 incidents change", an
"MTBF 0.0 hours", and a celebrated −13% response-time "improvement" measured on error
responses. Our report must never emit celebratory colors or meaningless stats for
degenerate periods.

Explicit product decision: **no plan-upsell content in report emails** — no
"available in Team plan" tips, no upgrade links. The report exists to make people like
the product, not to push purchases. (This rules out competitor idea #5 by design.)

## Proposal

All changes are content-level on the existing subsystem: builder (`uptimereport.Builder`),
view model (`uptimereport.Data` / `CheckRow`), template, and their tests. The delivery
job, schedules, and unsubscribe flow are untouched.

### 1. Period-over-period deltas

- Compute the window *before* the report window (add a small helper next to
  `slo.PreviousWindow` in `server/internal/slo/window.go` — calendar-aware, since
  "the month before" is not "minus 30 days") and run the same aggregation over it:
  `uptimebar.WindowAvailability` for uptime + avg duration, `IncidentBlock` for incidents.
- Add to `Data` (PascalCase JSON tags — see the load-bearing note at `report.go:37-52`):
  previous-period availability, availability delta (formatted with sign), incident-count
  delta, and avg-response-time delta in percent.
- Template renders each delta next to its headline stat, with color semantics:
  green only for a genuine improvement, red for a regression, neutral gray for "no
  change" / "not comparable" (see guards below). Zero is never green.

### 2. Response-time summary block

Org-level (scope-level) block with:

- **Average response time** — from the summed `BucketStats.AvgDuration()` the builder
  already accumulates into `overall` (`report.go:197-203`).
- **Min / Max** — raw rows carry per-sample `duration`; rollup rows carry
  `duration_min` / `duration_max`. Extend the stats carried back by the uptimebar read
  path with min/max, **as new fields only** — `BucketStats.AvailabilityPct` is pinned
  byte-identical for badges/status pages/SLOs (`bucketing.go:59-67`), so existing
  fields and methods must not change semantics.
- **Slow-sample count** — "N samples above the slow threshold" (default 1000ms,
  constant for now). Exact per-sample counting is only possible on the raw tier; for
  rollup tiers count buckets whose `duration_max` exceeds the threshold and phrase the
  stat accordingly (e.g. "peaks above 1s: N"), so the number is honest across tiers.
  State in the template copy what a "peak" is.

### 3. Average incident duration

`TotalDowntimeSeconds / Count` from the existing `PeriodIncidents` block
(`report.go:229-241`), formatted with the existing `formatDuration`. Shown only when
`Count > 0`. Deliberately **no MTBF**: with 0–2 incidents a month (the common case) MTBF
is a degenerate statistic (the competitor email's "0.0 hours" proves the point).

### 4. Worst-first check table with per-day strip

- Replace the name sort at `report.go:215` with: ascending availability, checks with
  `HasData=false` last, ties broken by name. The `maxCheckRows` truncation then keeps
  the *worst* 50 instead of the alphabetically-first 50 — which is the right bias for
  an email. Mention the truncation in the template when it happens ("showing the 50
  lowest-uptime checks of N") instead of silently capping.
- Add a per-day availability strip to each `CheckRow`: one cell per local day of the
  period (7 for weekly, 28–31 for monthly), colored by that day's availability, gray
  for no-data days. Day bucketing exists in `uptimebar` (`BucketAvailability`,
  `bucketing.go:550`); the builder resolves the schedule's timezone already
  (`report.go:139-146`).
- Email-client constraints: render the strip as a fixed-height `<td>` row (or inline
  `<span>`s) with inline `bgcolor`/`style` only — no flexbox, no JS, no external CSS.
  Keep the payload sane: the strip is per-row × up to 50 rows; verify the rendered
  email stays well under common size-clipping limits (Gmail clips ~102KB) with a
  50-check monthly fixture.

### Degenerate-case guards (applies to all of the above)

- **Down-all-period:** when overall availability is 0% (or a check was down its entire
  period), do not show the latency block for that scope — response times of error
  responses are noise. Prefer a plain factual headline ("`api.example.com` was down for
  the entire period") over a grid of zeros.
- **No previous-period data** (new org, new check, schedule's first run): omit the
  delta entirely — never render "±0.00%" against an empty baseline, and never in green.
- **Zero deltas:** neutral color, "no change" phrasing.
- **New check mid-period:** existing `HasData` handling stays; day cells before the
  check existed are gray, not red.
- Decide whether duration accumulation should exclude down/error samples going forward
  (raw accumulation currently adds any row with a non-nil `Duration`,
  `bucketing.go:159`; the aggregation job defines what rollup `duration_avg` includes —
  check `server/internal/jobs/jobtypes/job_aggregation.go` before choosing). Whatever
  the decision, apply it consistently between raw and rollup tiers and document it in
  the template copy if failed samples are included. Changing this must not alter what
  status pages/badges display — if their semantics would change, keep the existing
  fields and add report-specific ones.

### Non-goals

- No plan-upsell content, upgrade links, or entitlement-gated teasers in report emails.
- No MTBF.
- No per-recipient customization beyond the existing `UnsubscribeURL`.
- No changes to schedule CRUD, frequency options, or the delivery job.
- No configurable slow-threshold UI (a builder constant is fine for now; a follow-up
  spec can make it a schedule field).

### Testing

- Extend `TestUptimeReportRendersRealContent` (pins the JSON round-trip described at
  `report.go:37-52`) so every new `Data`/`CheckRow` field survives the
  marshal-to-job-config/unmarshal-to-map cycle — a mis-tagged field renders as
  `<no value>` silently.
- Table-driven builder tests for each degenerate case above (down-all-period, empty
  previous window, zero delta, mid-period check creation), asserting both the values
  *and* the suppression flags/colors — i.e. tests that prove the negatives.
- A worst-first sort test including no-data checks and the >50-row truncation.
- A size test (or at least an assertion in an existing render test) for the 50-check
  monthly fixture with strips.

## Implementation Plan

### 1. `server/internal/slo/window.go` — calendar-aware previous window

Add `PrecedingWindow(loc, window, weekly) Window`: the period immediately before an
already-resolved report window, built with `AddDate` in `loc` (so "the month before
March" is February, 28 days, and a DST month is honestly an hour short). `End` is the
given window's `Start` in `loc`, so the two windows are exactly adjacent and half-open.
Tests: month-length asymmetry (Mar → Feb), year rollover, weekly, DST.

### 2. `server/internal/uptimebar/bucketing.go` — min/max/slow, as NEW fields only

`BucketStats.AvailabilityPct` / `AvgDuration` / `Up` / `Total` / `DurCnt` / `DurSum` keep
byte-identical semantics — badges, status pages and SLOs must not move. Added:

- `DurMin`, `DurMax`, `DurExtremaCnt` — extremes over the bucket, valid only when
  `DurExtremaCnt > 0`. Raw rows fold their own `duration`; rollup rows fold
  `duration_min` / `duration_max`.
- `SlowSamples` — RAW samples strictly above `SlowSampleThresholdMillis` (1000, an
  exported constant; no UI, per the non-goals).
- `SlowPeaks` — ROLLUP rows whose `duration_max` exceeds the threshold. Counted
  separately from `SlowSamples` because they are a different unit (a rolled-up period
  with at least one slow sample, not a sample), and the report labels them as such.
- `DurationRange() (min, max float64, ok bool)` accessor.
- `Add` folds all five, so the group-merge path can never forget one.

**Failed-sample decision (spec's bounded choice):** raw accumulation
(`accumulateRaw`) and the aggregation job (`processRawResult`) BOTH already include any
non-lifecycle row carrying a duration — including `down` / `error` / `timeout`. Raw and
rollup therefore already agree, and changing either would move `duration_avg` for status
pages and badges. Decision: **keep it — failed samples are included** — and say so in
the template copy ("Response times include failed samples.").

### 3. `server/internal/uptimereport/report.go`

- **Previous window:** `slo.PrecedingWindow` + a second `uptimebar.WindowAvailability`
  and a second `incidentBlock`. `HasPreviousData` is `prevOverall.Total > 0`.
- **Deltas** (flat scalar view-model fields, not nested structs — the email-preview
  fixture is a hand-built `map[string]any` and `{{.X.Y}}` on a missing map key is a
  template ERROR, whereas a missing flat bool is just false):
  `ShowAvailabilityDelta` / `…Text` / `…Color`, same triplet for incidents and response
  time. Green only for a genuine improvement, red for a regression, neutral `#6b7280`
  for "no change"; **no previous data ⇒ `Show*` false, no text at all**.
- **Latency block:** `HasLatency`, `AvgResponseTime`, `MinResponseTime`,
  `MaxResponseTime`, `SlowCount`, `SlowLabel`, `SlowNote`, `LatencyNote`. Suppressed
  (`HasLatency=false`) when the scope was down the whole period or no duration was
  recorded.
- **Average incident duration:** `AverageIncident`, from the existing
  `PeriodIncidents.AverageSeconds`, only when `Count > 0`.
- **Down-all-period:** `DownAllPeriod` + `DownAllPeriodLabel` (the check's own name when
  the scope is a single check, else the scope label); per row, `CheckRow.DownAllPeriod`.
  Also suppresses the response-time delta — a delta measured on error responses is the
  competitor failure mode this spec exists to avoid.
- **Worst-first sort:** ascending availability, `HasData=false` last, ties by name.
  Sorted through a local keyed slice so the view model keeps no sort scaffolding.
  Truncation surfaces as `Truncated` / `TruncatedShown` / `TruncatedTotal`.
- **Per-day strip:** one `uptimebar.BucketAvailability` call at 24 h, aligned to the UTC
  day (`Truncate(24h)`), covering the window. **UTC days, not local days**: day-tier
  rollups are UTC-day aligned by construction, so folding them into local days would
  misattribute a whole day for every zone with a negative offset (and drop the last
  local day of a month entirely). The strip is labeled "(UTC)" rather than lying about
  it. Cells carry a discrete color (green/amber/orange/red, gray for no data) and are
  run-length encoded (`DayCell{Color, Span, Wide}`) — both for size and because a
  continuous gradient is illegible at 6 px.

### 4. `server/internal/email/templates/uptime-report.html`

Deltas next to the headline and in the details table; a response-time block; the
worst-first table with the strip as a second row per check; truncation note; legend.
Strip markup is a bare nested `<table>` with `<td bgcolor>` cells and **no class**, kept
OUT of `.details-table` — the formatter's CSS inliner would otherwise inline
`.details-table td { padding … }` into all ~1600 strip cells. Plain-text part mirrors
every new stat.

### 5. Tests

- `slo`: `PrecedingWindow` table test.
- `uptimebar`: min/max/slow accumulation across raw and rollup tiers, `Add` merge, and a
  pin that `AvailabilityPct`/`AvgDuration` are unchanged.
- `uptimereport/report_test.go`: delta formatting/colors (including the neutral-zero and
  omitted-baseline negatives), worst-first sort with no-data rows, RLE strip.
- `uptimereport/builder_test.go` (new, in-memory SQLite, real `db.Service`): end-to-end
  degenerate cases — down-all-period, empty previous window, zero delta, mid-period
  check creation, >50-row truncation — asserting suppression flags and neutral colors,
  each with a positive control proving the assertion could have failed.
- `uptimereport/render_test.go`: every new field in the JSON round trip, and a 50-check
  × 31-day monthly fixture asserted well under Gmail's ~102 KB clipping limit.
- `emailpreview`: fixture extended so the preview exercises the new blocks.
