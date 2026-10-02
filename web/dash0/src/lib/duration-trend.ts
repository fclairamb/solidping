import {
  getStartFor,
  type TimeRange,
  type ZoomWindow,
} from "@/lib/chart-window";
import type { DurationStats } from "@/lib/duration-stats";

const RANGE_MS: Record<TimeRange, number> = {
  hour: 3_600_000,
  day: 24 * 3_600_000,
  week: 7 * 24 * 3_600_000,
  month: 30 * 24 * 3_600_000,
};

/** The window of equal length ending where the current one starts: the zoom
 * window when one is active, else the default range (same start the chart uses). */
export function previousWindowFor(
  range: TimeRange,
  zoom?: ZoomWindow,
): ZoomWindow {
  const to = zoom ? zoom.from : Date.parse(getStartFor(range));
  const length = zoom ? zoom.to - zoom.from : RANGE_MS[range];

  return { from: to - length, to };
}

/** Below this relative change the trend reads "flat", so a stable check does
 * not flicker between +1% and -1% on every refetch. */
export const FLAT_THRESHOLD = 0.05;

/** The previous window must hold at least 1/MIN_SAMPLE_RATIO of the current
 * window's samples ("an order of magnitude fewer" means no comparison), so a
 * check created yesterday never reads +900%. */
export const MIN_SAMPLE_RATIO = 10;

export interface DurationTrend {
  direction: "up" | "down" | "flat";
  /** Signed, rounded percentage change of avg vs the previous window. */
  percent: number;
}

/**
 * Change of the average response time against the immediately preceding window
 * of equal length. Compares avg to avg on purpose: the p95 combination across
 * rollup rows is an approximation and a ratio of two approximations is noise.
 *
 * Maintenance windows: the existing avg (computeDurationStats and the server
 * aggregator's total_duration) does NOT exclude maintenance probes, so neither
 * does this comparison. It stays consistent with the strip next to it.
 *
 * Returns null ("say nothing rather than something wrong") when either window
 * has no data, the previous window has an order of magnitude fewer samples, or
 * its average is not a positive number.
 */
export function computeDurationTrend(
  current: DurationStats | null,
  previous: DurationStats | null,
): DurationTrend | null {
  if (!current || !previous) return null;
  if (previous.count <= 0 || previous.avg <= 0) return null;
  if (previous.count * MIN_SAMPLE_RATIO < current.count) return null;

  const change = (current.avg - previous.avg) / previous.avg;
  const percent = Math.round(change * 100);
  if (Math.abs(change) < FLAT_THRESHOLD || percent === 0) {
    return { direction: "flat", percent: 0 };
  }

  return { direction: change > 0 ? "up" : "down", percent };
}
