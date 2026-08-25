import { describe, expect, it } from "vitest";

import {
  chartFetchParams,
  chartRollupTier,
  getStartFor,
  seamStartFrom,
} from "@/lib/chart-window";

const ONE_HOUR = 3_600_000;
const ONE_MINUTE = 60_000;

function iso(msAgo: number): string {
  return new Date(Date.now() - msAgo).toISOString();
}

describe("seamStartFrom", () => {
  it("returns undefined when pass 1 produced nothing usable", () => {
    // A check younger than one rollup bucket has raw and NOTHING else — if the
    // seam were "now" here the chart would render empty for its whole first
    // hour of life.
    expect(seamStartFrom(undefined)).toBeUndefined();
    expect(seamStartFrom([])).toBeUndefined();
    expect(seamStartFrom([{ region: "eu" }])).toBeUndefined();
    expect(seamStartFrom([{ periodStart: "not-a-date" }])).toBeUndefined();
  });

  it("anchors on the newest rollup bucket the pass returned", () => {
    const newest = iso(ONE_HOUR);
    expect(
      seamStartFrom([
        { periodStart: iso(5 * ONE_HOUR), region: "eu" },
        { periodStart: newest, region: "eu" },
        { periodStart: iso(3 * ONE_HOUR), region: "eu" },
      ]),
    ).toBe(new Date(newest).toISOString());
  });

  it("widens the seam to the OLDEST per-region newest bucket", () => {
    // us is six hours behind eu. A global maximum would set the seam from eu
    // and leave us with neither a rollup nor raw over the difference — a
    // six-hour hole in one region's line that nothing else would fill.
    const lagging = iso(6 * ONE_HOUR);
    const seam = seamStartFrom([
      { periodStart: iso(ONE_HOUR), region: "eu" },
      { periodStart: iso(2 * ONE_HOUR), region: "eu" },
      { periodStart: lagging, region: "us" },
      { periodStart: iso(9 * ONE_HOUR), region: "us" },
    ]);

    expect(seam).toBe(new Date(lagging).toISOString());
  });

  it("treats rows with no region as one group", () => {
    const newest = iso(ONE_HOUR);
    expect(
      seamStartFrom([{ periodStart: iso(4 * ONE_HOUR) }, { periodStart: newest }]),
    ).toBe(new Date(newest).toISOString());
  });
});

describe("chartFetchParams raw seam", () => {
  it("narrows raw to the seam on a month view while the rollup keeps the window", () => {
    const seam = iso(ONE_HOUR);
    const plan = chartFetchParams("month", ONE_MINUTE, undefined, seam);
    const [rollup, raw] = plan;

    expect(plan).toHaveLength(2);
    expect(rollup.periodType).toBe("hour,day");
    expect(raw.periodType).toBe("raw");

    const windowStart = rollup.periodStartAfter;
    expect(raw.periodStartAfter).toBe(seam);
    expect(new Date(raw.periodStartAfter).getTime()).toBeGreaterThan(
      new Date(windowStart).getTime(),
    );

    // The point of the whole spec, stated as arithmetic: a 1-minute check in
    // 3 regions over the seam fits in ONE page of 1000, where the same check
    // over the raw-retention window (24 h) would need five.
    const pointsInSeam =
      ((Date.now() - new Date(raw.periodStartAfter).getTime()) / ONE_MINUTE) * 3;
    expect(pointsInSeam).toBeLessThanOrEqual(raw.size);
    expect((24 * 60 * 3) / raw.size).toBeGreaterThan(4);
  });

  it("ignores the seam when raw IS the tier for the range", () => {
    // Positive control for the narrowing: on an hour view (and on a day view of
    // a check slower than 5 min) there is no rollup pass, so narrowing raw
    // would delete the chart's data rather than its redundancy.
    for (const [range, periodMs] of [
      ["hour", ONE_MINUTE],
      ["day", 15 * ONE_MINUTE],
    ] as const) {
      expect(chartRollupTier(range, periodMs)).toBe("");

      const plan = chartFetchParams(range, periodMs, undefined, iso(ONE_HOUR));
      expect(plan).toHaveLength(1);
      expect(plan[0].periodType).toBe("raw");
      expect(plan[0].periodStartAfter).toBe(getStartFor(range));
    }
  });

  it("never lets a seam widen the fetch past the requested window", () => {
    // A seam older than the window start (a stale rollup page from a wider
    // range still in cache) must not turn into a wider raw query.
    const plan = chartFetchParams("week", ONE_MINUTE, undefined, iso(90 * 24 * ONE_HOUR));
    const [rollup, raw] = plan;
    expect(raw.periodStartAfter).toBe(rollup.periodStartAfter);
  });

  it("keeps the zoom's upper bound on the narrowed raw entry", () => {
    const to = Date.now();
    const from = to - 30 * 24 * ONE_HOUR;
    const seam = new Date(to - ONE_HOUR).toISOString();
    const plan = chartFetchParams("month", ONE_MINUTE, { from, to }, seam);

    for (const entry of plan) {
      expect(entry.periodEndBefore).toBe(new Date(to).toISOString());
    }
    expect(plan[1].periodStartAfter).toBe(seam);
    expect(plan[0].periodStartAfter).toBe(new Date(from).toISOString());
  });

  it("still asks for raw over the full window when there is no seam", () => {
    // Pass 1 empty must not short-circuit pass 2 (brand-new check).
    const plan = chartFetchParams("month", ONE_MINUTE, undefined, undefined);
    expect(plan[1].periodStartAfter).toBe(plan[0].periodStartAfter);
  });
});
