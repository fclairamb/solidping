import { describe, expect, it } from "vitest";
import type { OrgResult } from "@/api/hooks";
import { computeDurationStats } from "./duration-stats";

const raw = (region: string, durationMs: number): OrgResult =>
  ({ region, periodType: "raw", durationMs }) as OrgResult;

describe("computeDurationStats", () => {
  it("returns null without points", () => {
    expect(computeDurationStats([], undefined)).toBeNull();
  });

  it("is exact for a single region of raw rows", () => {
    const s = computeDurationStats([raw("eu", 10), raw("eu", 30)], undefined);
    expect(s).toMatchObject({
      min: 10,
      max: 30,
      avg: 20,
      count: 2,
      isEstimate: false,
    });
  });

  it("flags an estimate when more than one region contributes", () => {
    const s = computeDurationStats(
      [raw("eu", 10), raw("eu", 30), raw("us", 100)],
      undefined,
    );
    expect(s?.isEstimate).toBe(true);
    expect(s?.min).toBe(10);
    expect(s?.max).toBe(100);
    expect(s?.count).toBe(3);
  });

  it("stays exact when filtered to one region of a multi-region window", () => {
    const s = computeDurationStats([raw("eu", 10), raw("us", 100)], "eu");
    expect(s).toMatchObject({ min: 10, max: 10, isEstimate: false });
  });

  it("weights avg by totalChecks for rollup rows and marks estimate", () => {
    const rollup = (region: string, avg: number, n: number): OrgResult =>
      ({
        region,
        periodType: "hour",
        durationAvgMs: avg,
        durationMinMs: avg,
        durationMaxMs: avg,
        durationP95Ms: avg,
        totalChecks: n,
      }) as OrgResult;
    const s = computeDurationStats(
      [rollup("eu", 10, 9), rollup("us", 100, 1)],
      undefined,
    );
    expect(s?.avg).toBeCloseTo(19);
    expect(s?.isEstimate).toBe(true);
  });
});
