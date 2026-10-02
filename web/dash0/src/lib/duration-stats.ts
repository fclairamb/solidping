import type { OrgResult } from "@/api/hooks";

export interface DurationStats {
  min: number;
  max: number;
  avg: number;
  p95: number;
  count: number;
  /** True when the window includes any rollup (non-raw) row, so avg/p95 are
   * combined estimates rather than exact values (display with a `~` prefix). */
  isEstimate: boolean;
}

/**
 * Tier-aware min/avg/max/p95 + sample count for one region (or all regions
 * when `region` is undefined) over the given result set; the stats strip
 * renders both scopes. Mirrors the combination method
 * server/internal/jobs/jobtypes/job_aggregation.go actually uses for
 * combining child buckets:
 *   - min/max: exact min-of-mins / max-of-maxes across all contributing rows
 *     (raw rows contribute their own durationMs as both min and max).
 *   - avg: totalChecks-weighted mean of each rollup row's durationAvgMs
 *     (falling back to its plotted durationMs when durationAvgMs is missing —
 *     e.g. a rollup row that predates this field), each raw row contributing
 *     its own durationMs with weight 1.
 *   - p95: an unweighted average of each row's own p95 — rollup rows
 *     contribute their stored durationP95Ms (falling back to durationMs when
 *     absent), raw rows contribute their own durationMs as a degenerate
 *     single-sample p95. This mirrors calculateAggregatedMetrics's plain
 *     p95Sum / p95Count combination (NOT totalChecks-weighted — the
 *     aggregator does not weight p95 by count when combining buckets).
 * Multi-region fold (region undefined, several regions in the window): the
 * rules above apply across every region's rows. min/max are the extremes over
 * all regions, avg is totalChecks-weighted so a dense region is not out-voted
 * by a sparse one, and p95 is the unweighted mean of per-row p95s, which is
 * not a true percentile across regions with different latencies, so
 * isEstimate is also set whenever more than one region contributes.
 * Returns null when there is no duration data for the region in this window.
 */
export function computeDurationStats(
  allPoints: OrgResult[],
  region: string | undefined,
): DurationStats | null {
  const points = region
    ? allPoints.filter((p) => p.region === region)
    : allPoints;
  if (points.length === 0) return null;

  let min = Infinity;
  let max = -Infinity;
  let count = 0;
  let avgWeightedSum = 0;
  let avgWeight = 0;
  let p95Sum = 0;
  let p95Count = 0;
  let isEstimate = new Set(points.map((p) => p.region ?? "")).size > 1;

  for (const p of points) {
    const isRaw = p.periodType === "raw" || !p.periodType;

    if (isRaw) {
      if (p.durationMs == null) continue;
      min = Math.min(min, p.durationMs);
      max = Math.max(max, p.durationMs);
      count += 1;
      avgWeightedSum += p.durationMs;
      avgWeight += 1;
      p95Sum += p.durationMs;
      p95Count += 1;
      continue;
    }

    // Rollup row (hour/day/month) — combined stats become estimates.
    isEstimate = true;
    const weight = p.totalChecks ?? 1;
    count += weight;

    if (p.durationMinMs != null) min = Math.min(min, p.durationMinMs);
    if (p.durationMaxMs != null) max = Math.max(max, p.durationMaxMs);

    const avgFallback = p.durationAvgMs ?? p.durationMs;
    if (avgFallback != null) {
      avgWeightedSum += avgFallback * weight;
      avgWeight += weight;
    }

    const p95Fallback = p.durationP95Ms ?? p.durationMs;
    if (p95Fallback != null) {
      p95Sum += p95Fallback;
      p95Count += 1;
    }
  }

  if (
    !Number.isFinite(min) ||
    !Number.isFinite(max) ||
    avgWeight === 0 ||
    p95Count === 0
  ) {
    return null;
  }

  return {
    min,
    max,
    avg: avgWeightedSum / avgWeight,
    p95: p95Sum / p95Count,
    count,
    isEstimate,
  };
}
