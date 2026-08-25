/**
 * Seam continuity (spec 2026-08-22-07 §2). Since the raw pass now covers only
 * the span between the newest rollup bucket and now, a rollup→raw transition is
 * the NORMAL shape of every wide range rather than an edge case. detectGaps
 * already refuses to call a tier change a gap; that rule now carries the whole
 * chart, so it gets pinned instead of assumed.
 */
import { describe, expect, it } from "vitest";

import { detectGaps, type ChartPoint } from "@/components/checks/response-time-chart";
import { seamStartFrom } from "@/lib/chart-window";
import { mergeResultTiers } from "@/lib/result-tiers";

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

interface Row {
  uid: string;
  region: string;
  periodType: string;
  periodStart: string;
  periodEnd?: string;
}

const NOW = Date.UTC(2026, 7, 22, 12, 17, 0);
/** Hourly buckets for two regions, newest closing at 12:00. */
const ROLLUP_BUCKETS: Row[] = ["eu", "us"].flatMap((region) =>
  Array.from({ length: 6 }, (_, i) => {
    const start = Date.UTC(2026, 7, 22, 11 - i, 0, 0);

    return {
      uid: `h-${region}-${i}`,
      region,
      periodType: "hour",
      periodStart: new Date(start).toISOString(),
      periodEnd: new Date(start + ONE_HOUR).toISOString(),
    };
  }),
);

/** Every raw row the table holds — deliberately covering the rolled-up hours
 * too, so a seam that is one bucket too wide pulls duplicates in. */
const ALL_RAW: Row[] = ["eu", "us"].flatMap((region) =>
  Array.from({ length: 6 * 60 + 17 }, (_, i) => ({
    uid: `r-${region}-${i}`,
    region,
    periodType: "raw",
    periodStart: new Date(NOW - i * ONE_MINUTE).toISOString(),
  })),
);

/** The server's own filter: `period_start >= periodStartAfter`. */
function serveRaw(periodStartAfter: string): Row[] {
  const bound = Date.parse(periodStartAfter);

  return ALL_RAW.filter((r) => Date.parse(r.periodStart) >= bound);
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

  it("never lets the two tiers cover the same span, even if raw was not deleted", () => {
    // The clause the spec names: "no duplicated point where the two tiers
    // meet". The fixture is deliberately adversarial — raw rows exist at a
    // 1-minute cadence across the WHOLE span, including the hours the rollup
    // buckets already represent. In production the aggregator deletes those in
    // the same transaction that writes the bucket, but that is an invariant of
    // a different component; the seam has to make the tiers disjoint on its own.
    //
    // This runs the real pipeline: seamStartFrom over pass-1 rows → the raw
    // window → a server that honours `period_start >= periodStartAfter` →
    // mergeResultTiers. Anchoring the seam on periodStart instead of the
    // bucket's exclusive end makes the newest bucket fall inside BOTH windows
    // and this fails with 60 overlapping raw rows.
    const rollups = ROLLUP_BUCKETS;
    const seam = seamStartFrom(rollups)!;
    expect(seam).toBeTruthy();

    const served = serveRaw(seam);
    const merged = mergeResultTiers([rollups, served]);

    // Positive control: raw IS fetched and DOES reach the series. Without it an
    // empty raw tier would satisfy every disjointness assertion below.
    expect(served.length).toBeGreaterThan(30);
    expect(merged.filter((r) => r.periodType === "raw").length).toBe(
      served.length,
    );
    expect(merged.filter((r) => r.periodType === "hour").length).toBe(
      rollups.length,
    );

    // Disjoint in time: no raw point sits inside a span a rollup bucket covers.
    const bucketEnd = Math.max(
      ...rollups.map((r) => Date.parse(r.periodEnd!)),
    );
    for (const row of served) {
      expect(
        Date.parse(row.periodStart!),
        `raw row ${row.uid} falls inside a rolled-up bucket`,
      ).toBeGreaterThanOrEqual(bucketEnd);
    }

    // …and no gap either: raw resumes within one cadence of the bucket edge.
    const firstRaw = Math.min(...served.map((r) => Date.parse(r.periodStart!)));
    expect(firstRaw - bucketEnd).toBeLessThanOrEqual(ONE_MINUTE);

    // No timestamp is drawn twice.
    const seen = merged.map((r) => `${r.region}@${r.periodStart}`);
    expect(new Set(seen).size).toBe(seen.length);
  });
});
