import { describe, expect, it } from "vitest";
import type { DurationStats } from "@/lib/duration-stats";
import { computeDurationTrend, previousWindowFor } from "./duration-trend";

const stats = (avg: number, count: number): DurationStats => ({
  min: 1,
  max: 1000,
  avg,
  p95: avg,
  p50: avg,
  count,
  isEstimate: false,
});

describe("computeDurationTrend", () => {
  it("reports slower as a positive percentage", () => {
    expect(computeDurationTrend(stats(118, 100), stats(100, 100))).toEqual({
      direction: "up",
      percent: 18,
    });
  });

  it("reports faster as a negative percentage", () => {
    expect(computeDurationTrend(stats(80, 100), stats(100, 100))).toEqual({
      direction: "down",
      percent: -20,
    });
  });

  it("renders flat below the 5% threshold", () => {
    expect(computeDurationTrend(stats(103, 100), stats(100, 100))).toEqual({
      direction: "flat",
      percent: 0,
    });
    expect(
      computeDurationTrend(stats(97, 100), stats(100, 100))?.direction,
    ).toBe("flat");
  });

  it("says nothing without a previous or current window", () => {
    expect(computeDurationTrend(stats(100, 10), null)).toBeNull();
    expect(computeDurationTrend(null, stats(100, 10))).toBeNull();
  });

  it("says nothing when the previous window has an order of magnitude fewer samples", () => {
    expect(computeDurationTrend(stats(1000, 1000), stats(100, 99))).toBeNull();
    expect(
      computeDurationTrend(stats(1000, 1000), stats(100, 100)),
    ).not.toBeNull();
  });

  it("says nothing when the previous average is zero", () => {
    expect(computeDurationTrend(stats(10, 10), stats(0, 10))).toBeNull();
  });
});

describe("previousWindowFor", () => {
  it("ends where a zoom window starts and has the same length", () => {
    expect(previousWindowFor("day", { from: 5000, to: 8000 })).toEqual({
      from: 2000,
      to: 5000,
    });
  });

  it("covers the 24h before the current day window", () => {
    const w = previousWindowFor("day");
    expect(w.to - w.from).toBe(24 * 3_600_000);
    expect(Math.abs(Date.now() - 24 * 3_600_000 - w.to)).toBeLessThan(61_000);
  });
});
