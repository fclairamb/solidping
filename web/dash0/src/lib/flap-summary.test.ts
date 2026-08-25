import { describe, expect, it } from "vitest";
import type { Check } from "@/api/hooks";
import { flappingSummaryParams } from "@/lib/flap-summary";

function baseCheck(overrides: Partial<Check> = {}): Check {
  return {
    uid: "check-1",
    flappingWindowSeconds: 21600, // 6h
    flapBackoffFactor: 2,
    maxRecoveryMultiplier: 8,
    recoveryPeriodSeconds: 120, // 2 min
    ...overrides,
  };
}

describe("flappingSummaryParams", () => {
  it("returns undefined when the check has no flapState (feature off or nothing accumulated)", () => {
    expect(flappingSummaryParams(baseCheck())).toBeUndefined();
    expect(flappingSummaryParams(baseCheck({ flapState: null }))).toBeUndefined();
  });

  it("reports the 2nd-outage case (flapCount 1) with an escalated multiplier", () => {
    const check = baseCheck({
      flapState: { flapCount: 1, effectiveRecoveryPeriodSeconds: 240 }, // R=120 * F^1=2 -> 240s
    });
    expect(flappingSummaryParams(check)).toEqual({
      count: 2, // flapCount + 1
      window: "6 h",
      multiplier: 2,
      effective: "4 min",
    });
  });

  it("reports a later escalation (flapCount 2)", () => {
    const check = baseCheck({
      flapState: { flapCount: 2, effectiveRecoveryPeriodSeconds: 480 }, // R=120 * F^2=4 -> 480s
    });
    const result = flappingSummaryParams(check);
    expect(result?.count).toBe(3);
    expect(result?.multiplier).toBe(4);
    expect(result?.effective).toBe("8 min");
  });

  it("drops the multiplier when the base recovery period is 0 (immediate resolve)", () => {
    const check = baseCheck({
      recoveryPeriodSeconds: 0,
      flapState: { flapCount: 1, effectiveRecoveryPeriodSeconds: 0 },
    });
    const result = flappingSummaryParams(check);
    expect(result?.multiplier).toBeUndefined();
    expect(result?.count).toBe(2);
  });

  it("renders an empty window label when flappingWindowSeconds is absent", () => {
    const check = baseCheck({
      flappingWindowSeconds: undefined,
      flapState: { flapCount: 1, effectiveRecoveryPeriodSeconds: 240 },
    });
    expect(flappingSummaryParams(check)?.window).toBe("");
  });
});
