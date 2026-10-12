import { HTTP_TIMING_PHASES, type HttpTimingPhaseId } from "@/lib/http-timing";

export interface TimingSeriesRow {
  periodStart?: string;
  metrics?: Record<string, unknown> | null;
}

export type TimingPoint = { ts: number } & Partial<
  Record<HttpTimingPhaseId, number>
>;

/**
 * Per-bucket phase averages, raw and rollup rows alike. Rows sharing a
 * timestamp (one per region) are averaged per phase over the rows that carry
 * it; a phase no row carries stays undefined (drawn as 0 in a stacked area).
 */
export function buildTimingSeries(rows: TimingSeriesRow[]): TimingPoint[] {
  const buckets = new Map<number, Map<HttpTimingPhaseId, number[]>>();
  for (const row of rows) {
    if (!row.periodStart || !row.metrics) continue;
    const ts = new Date(row.periodStart).getTime();
    if (!Number.isFinite(ts)) continue;
    for (const { id, key } of HTTP_TIMING_PHASES) {
      const value = row.metrics[key];
      if (typeof value !== "number" || !Number.isFinite(value)) continue;
      let phases = buckets.get(ts);
      if (!phases) {
        phases = new Map();
        buckets.set(ts, phases);
      }
      phases.set(id, [...(phases.get(id) ?? []), value]);
    }
  }
  return [...buckets.entries()]
    .sort(([a], [b]) => a - b)
    .map(([ts, phases]) => {
      const point: TimingPoint = { ts };
      for (const [id, values] of phases) {
        point[id] = values.reduce((sum, v) => sum + v, 0) / values.length;
      }
      return point;
    });
}
