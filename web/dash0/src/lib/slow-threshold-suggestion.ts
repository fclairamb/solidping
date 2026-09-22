import type { OrgResult } from "@/api/hooks";

/**
 * Degraded detection's slow rule needs a threshold, and there is deliberately no
 * auto-baselining (spec 2026-09-22-03: "No auto-baselined thresholds. The form
 * suggests a value from `DurationP95` and the operator commits to it").
 *
 * This is that suggestion, and nothing more: the form shows it, the operator
 * decides. On the motivating check — a 453 ms baseline p95 — it lands on 900 ms,
 * and the spec's own replay says 1000 ms detects the episode at 14:37 while
 * 1400 ms waits until 14:47. Two times p95 is the multiplier the spec names.
 */
export const SLOW_THRESHOLD_P95_MULTIPLIER = 2;

/**
 * Rounding granularity. A suggestion of "907 ms" reads like a measurement the
 * operator must not disturb; "900 ms" reads like the round number it is.
 */
const SLOW_THRESHOLD_ROUNDING_MS = 50;

/**
 * Below this there is no honest suggestion to make: a handful of samples over a
 * few minutes describes whatever the network was doing at that moment, not the
 * check's baseline.
 */
const MIN_SUGGESTION_SAMPLES = 3;

/**
 * suggestSlowThresholdMs returns ~2x the check's observed p95, rounded, or null
 * when the window carries too little data to say anything.
 *
 * The p95 combination mirrors what the aggregation job does when it folds
 * buckets together (an unweighted mean of each row's own p95, raw rows
 * contributing their single duration as a degenerate p95) so the number the form
 * suggests cannot disagree with the number the check page shows.
 */
export function suggestSlowThresholdMs(
  rows: OrgResult[] | undefined,
): number | null {
  if (!rows || rows.length === 0) return null;

  let sum = 0;
  let count = 0;

  for (const row of rows) {
    const isRaw = row.periodType === "raw" || !row.periodType;
    const value = isRaw
      ? row.durationMs
      : (row.durationP95Ms ?? row.durationMs);

    if (value == null || !Number.isFinite(value) || value <= 0) continue;

    sum += value;
    count += 1;
  }

  if (count < MIN_SUGGESTION_SAMPLES) return null;

  const p95 = sum / count;
  const suggestion = p95 * SLOW_THRESHOLD_P95_MULTIPLIER;

  const rounded =
    Math.round(suggestion / SLOW_THRESHOLD_ROUNDING_MS) *
    SLOW_THRESHOLD_ROUNDING_MS;

  // A very fast check would otherwise be suggested a 0 ms threshold, which is
  // the documented "slow rule off" value — the opposite of what the form means.
  return Math.max(rounded, SLOW_THRESHOLD_ROUNDING_MS);
}
