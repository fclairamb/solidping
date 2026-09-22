import { describe, expect, it } from "vitest";
import type { OrgResult } from "@/api/hooks";
import { suggestSlowThresholdMs } from "./slow-threshold-suggestion";

function hourRow(p95: number): OrgResult {
  return { periodType: "hour", durationP95Ms: p95, totalChecks: 60 };
}

function rawRow(duration: number): OrgResult {
  return { periodType: "raw", durationMs: duration };
}

describe("suggestSlowThresholdMs", () => {
  it("suggests about twice the observed p95, rounded", () => {
    // The motivating episode's own baseline: 453 ms p95 → 900 ms.
    expect(
      suggestSlowThresholdMs([hourRow(453), hourRow(453), hourRow(453)]),
    ).toBe(900);
  });

  it("suggests a value that would have caught the motivating episode", () => {
    // The spec's replay: 1000 ms detects at 14:37, 1400 ms only at 14:47. The
    // suggestion must land on the early side of that pair, not the late one.
    const suggestion = suggestSlowThresholdMs([
      hourRow(453),
      hourRow(460),
      hourRow(440),
      hourRow(470),
    ]);

    expect(suggestion).not.toBeNull();
    expect(suggestion as number).toBeLessThan(1400);
  });

  it("reads a raw row's own duration as a degenerate p95", () => {
    expect(
      suggestSlowThresholdMs([rawRow(100), rawRow(100), rawRow(100)]),
    ).toBe(200);
  });

  it("falls back to a rollup row's plotted duration when p95 is absent", () => {
    const rows: OrgResult[] = [
      { periodType: "hour", durationMs: 300 },
      { periodType: "hour", durationMs: 300 },
      { periodType: "hour", durationMs: 300 },
    ];
    expect(suggestSlowThresholdMs(rows)).toBe(600);
  });

  it("returns null when there is not enough data to be honest", () => {
    expect(suggestSlowThresholdMs(undefined)).toBeNull();
    expect(suggestSlowThresholdMs([])).toBeNull();
    // Two samples describe the last two minutes, not a baseline.
    expect(suggestSlowThresholdMs([hourRow(500), hourRow(500)])).toBeNull();
  });

  it("ignores rows with no usable duration", () => {
    const rows: OrgResult[] = [
      hourRow(500),
      { periodType: "hour" },
      { periodType: "raw", durationMs: 0 },
      hourRow(500),
      hourRow(500),
    ];
    expect(suggestSlowThresholdMs(rows)).toBe(1000);
  });

  it("never suggests 0, which is the documented off value", () => {
    // A sub-millisecond local check would otherwise round to zero and silently
    // mean "slow rule off" — the opposite of what a suggestion is for.
    const rows = [rawRow(0.4), rawRow(0.4), rawRow(0.4)];
    expect(suggestSlowThresholdMs(rows)).toBe(50);
  });
});
