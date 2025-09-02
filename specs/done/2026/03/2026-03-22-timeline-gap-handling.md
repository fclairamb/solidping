# Timeline Gap Handling for Response Time Charts

## Overview

When the Response Times chart has large gaps in data (e.g., a check created recently but viewed in "Week" mode, or monitoring paused/restarted), the chart renders poorly: data is compressed into a tiny portion, and x-axis labels overlap. This spec describes how to handle these gaps gracefully.

## Motivation

The current linear time scale spreads the full domain evenly. When data only occupies a small fraction of the domain, the chart becomes unreadable. Users selecting "Week" or "Month" views for recently-created checks see most of the chart empty with data squished to one side.

## Implementation

### Gap Detection

A gap is defined as a period between consecutive data points exceeding 5x the median check interval. This adapts to the check's configured frequency without hardcoding thresholds.

**Algorithm:**
1. Calculate intervals between consecutive sorted data points
2. Compute the median interval (= typical check frequency)
3. Flag any interval > 5x median as a gap

### Null Insertion at Gap Boundaries

Insert null `durationMs` markers at gap boundaries in both `fullRange` and non-`fullRange` modes. Currently only done for full-range start/end boundaries. Combined with `connectNulls={false}`, this breaks the line/fill at every detected gap.

### Smart X-Axis Ticks

Compute an explicit `ticks` array instead of relying on Recharts auto-placement:
- **Sparse data** (< 30% of domain): ticks at domain boundaries + data cluster start/end
- **Normal data**: evenly-spaced ticks across the domain
- Always use `minTickGap={50}` to prevent label overlap

### Visual Gap Indicators

Use Recharts `ReferenceArea` to shade gap regions with a subtle muted fill. Show a "No data" label only for gaps wider than 10% of the domain to avoid visual clutter.

### Cluster-Aware Tick Formatting

Use the actual data span (not the full domain span) for choosing the tick format, so labels within data clusters show appropriate precision (e.g., "HH:mm" for hours of data even when the domain spans a week).

## Key Files

- `apps/dash0/src/components/checks/response-time-chart.tsx` — all changes in this single file

## Verification

1. Start dev environment: `make dev-test`
2. Create a new check, wait a few minutes, view in "Week" mode with Full Range ON → data cluster on right, "No data" shading on left, readable labels
3. View same check with Full Range OFF → chart auto-zooms to data extent
4. Lint: `cd apps/dash0 && bun run lint`

**Status**: In Progress | **Created**: 2026-03-22

---

## Implementation Plan

### Step 1: Add gap detection and null insertion
- Detect gaps (>5x median interval) in data points
- Insert null markers at gap boundaries in both full-range and non-full-range modes

### Step 2: Smart x-axis ticks and cluster-aware formatting
- Compute explicit ticks array instead of relying on Recharts auto-placement
- Use data span for tick format precision

### Step 3: Visual gap indicators
- Add ReferenceArea shading for gap regions
- Show "No data" label for wide gaps
