/**
 * Seam continuity (spec 2026-08-22-07 §2). Since the raw pass now covers only
 * the span between the newest rollup bucket and now, a rollup→raw transition is
 * the NORMAL shape of every wide range rather than an edge case. detectGaps
 * already refuses to call a tier change a gap; that rule now carries the whole
 * chart, so it gets pinned instead of assumed.
 */
import { describe, expect, it } from "vitest";

import { detectGaps, type ChartPoint } from "@/components/checks/response-time-chart";

const ONE_MINUTE = 60_000;
const ONE_HOUR = 3_600_000;

function point(ts: number, periodType: string): ChartPoint {
  return { ts, durationMs: 100, status: "up", periodType, region: "eu" };
}

/** 24 hourly rollup buckets ending at `seam`, then raw at the check's period
 * from `seam` to `end` — exactly what the two passes now merge into. */
function seamSeries(seam: number, end: number): ChartPoint[] {
  const rollups: ChartPoint[] = [];
  for (let i = 24; i >= 1; i--) rollups.push(point(seam - i * ONE_HOUR, "hour"));

  const raw: ChartPoint[] = [];
  for (let ts = seam; ts <= end; ts += ONE_MINUTE) raw.push(point(ts, "raw"));

  return [...rollups, ...raw];
}

describe("rollup → raw seam", () => {
  const seam = Date.UTC(2026, 7, 22, 11, 0, 0);
  const end = seam + ONE_HOUR;

  it("reports no gap across the tier transition", () => {
    const series = seamSeries(seam, end);
    const gaps = detectGaps(series, series[0].ts, end);

    expect(gaps).toEqual([]);
  });

  it("still reports a real gap INSIDE a tier", () => {
    // The positive control. Without it, a detectGaps that returned [] for
    // everything would satisfy the assertion above.
    const series = seamSeries(seam, end);
    const withHole = series.filter(
      (p) => !(p.periodType === "raw" && p.ts > seam + 5 * ONE_MINUTE && p.ts < seam + 40 * ONE_MINUTE),
    );
    const gaps = detectGaps(withHole, withHole[0].ts, end);

    expect(gaps).toHaveLength(1);
    expect(gaps[0].x1).toBe(seam + 5 * ONE_MINUTE);
    expect(gaps[0].x2).toBe(seam + 40 * ONE_MINUTE);
  });

  it("draws no duplicate point where the tiers meet", () => {
    // The aggregator compacts a bucket and deletes its source raw rows in one
    // transaction, so the tiers are disjoint in time and the newest rollup
    // bucket's own span carries no raw. A duplicated timestamp here would mean
    // the seam was computed from the wrong edge.
    const series = seamSeries(seam, end);
    const timestamps = series.map((p) => p.ts);

    expect(new Set(timestamps).size).toBe(timestamps.length);
    expect(series.filter((p) => p.periodType === "hour").at(-1)!.ts).toBe(
      seam - ONE_HOUR,
    );
    expect(series.find((p) => p.periodType === "raw")!.ts).toBe(seam);
  });
});
