/**
 * Bucket geometry for the chart's availability strip.
 *
 * The rules here are the spec's, not judgment calls (spec 2026-08-26-10,
 * "Resolved open questions"): day → 1h (24 cells), week → 6h (28 cells),
 * month → 1d (~30 cells), and a drag-zoom span → the smallest hour-multiple
 * keeping the cell count at or below 60. The **1-hour floor holds in every
 * case** — below an hour a percentage is noise, because a 5-minute slice of a
 * 1-minute check quantizes to 0/20/40/…% and the cell colour swings on one probe.
 */

import type { TimeRange } from "./chart-window";

export const ONE_HOUR_SECONDS = 3600;

/**
 * Below this span the strip is NOT rendered at all: the chart header shows a
 * single window-availability figure instead. A one-cell-wide strip reads as a
 * broken 24-cell strip, and putting the fallback in the header means it does not
 * reflow when a drag-zoom crosses the boundary.
 */
export const STRIP_MIN_SPAN_MS = 3 * 60 * 60 * 1000;

/** The cell-count ceiling for a zoom span. */
export const MAX_STRIP_CELLS = 60;

/** Fixed widths for the chart's preset ranges, in seconds. */
const PRESET_BUCKET_SECONDS: Record<TimeRange, number> = {
  // An hour view renders no strip at all; the value is here only so the map is
  // total and a caller that asks anyway gets the floor rather than NaN.
  hour: ONE_HOUR_SECONDS,
  day: ONE_HOUR_SECONDS,
  week: 6 * ONE_HOUR_SECONDS,
  month: 24 * ONE_HOUR_SECONDS,
};

/**
 * The smallest whole-hour multiple that cuts `spanMs` into at most
 * MAX_STRIP_CELLS cells. Always at least one hour.
 */
export function bucketSecondsForSpan(spanMs: number): number {
  if (!Number.isFinite(spanMs) || spanMs <= 0) return ONE_HOUR_SECONDS;
  const hours = Math.ceil(spanMs / 3_600_000 / MAX_STRIP_CELLS);
  return Math.max(1, hours) * ONE_HOUR_SECONDS;
}

/**
 * The strip's bucket width for the current window: the preset width when the
 * chart is showing one of its default ranges, the zoom rule when a drag-zoom is
 * active.
 */
export function stripBucketSeconds(
  timeRange: TimeRange,
  zoomSpanMs?: number,
): number {
  if (zoomSpanMs != null && zoomSpanMs > 0)
    return bucketSecondsForSpan(zoomSpanMs);
  return PRESET_BUCKET_SECONDS[timeRange] ?? ONE_HOUR_SECONDS;
}

/** True when the window is wide enough for a legible multi-cell strip. */
export function shouldRenderStrip(spanMs: number): boolean {
  return Number.isFinite(spanMs) && spanMs >= STRIP_MIN_SPAN_MS;
}

/**
 * The chart's own left gutter in pixels: recharts' `<YAxis width={60}>` plus the
 * `<AreaChart>` default 5px left margin. The strip is inset by exactly this so
 * its cells sit under the plot area rather than under the y-axis labels.
 *
 * **If the chart's YAxis width or margin changes, change this too** — nothing
 * but this comment links the two.
 */
export const CHART_PLOT_INSET_LEFT = 65;
/** The `<AreaChart>` default right margin. */
export const CHART_PLOT_INSET_RIGHT = 5;
