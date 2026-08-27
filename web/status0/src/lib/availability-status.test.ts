import { describe, test, expect } from "bun:test";
import {
  AVAILABILITY_THRESHOLD_DEGRADED,
  AVAILABILITY_THRESHOLD_UP,
  availabilityFill,
  classifyAvailabilityCounts,
  formatAvailabilityPct,
} from "./availability-status";

// The Go authority is uptimebar.Classify (server/internal/uptimebar/classify.go).
// These tests pin the TypeScript twin used for the ONE case the server cannot
// answer: a multi-region response-time slot merged on the client.

describe("classifyAvailabilityCounts", () => {
  test("mirrors the server's default thresholds", () => {
    expect(AVAILABILITY_THRESHOLD_UP).toBe(99.9);
    expect(AVAILABILITY_THRESHOLD_DEGRADED).toBe(99.0);
  });

  test("treats an empty slot as no-data, never as 100%", () => {
    expect(classifyAvailabilityCounts(0, 0)).toBe("noData");
    // The positive control: a genuinely perfect slot IS up, so "noData" above
    // is about absence and not a blanket fallback.
    expect(classifyAvailabilityCounts(60, 60)).toBe("up");
  });

  test("never paints a single failed sample red", () => {
    // 59/60 = 98.3%, well under the degraded threshold, but one failure only.
    expect(classifyAvailabilityCounts(59, 60)).toBe("degraded");
    // Two failures may go red — proving the guard is a guard and not a blanket
    // refusal to ever classify down.
    expect(classifyAvailabilityCounts(58, 60)).toBe("down");
  });

  test("honours the page's own thresholds", () => {
    // 55/60 = 91.7%: red on the defaults, green on a lenient page.
    expect(classifyAvailabilityCounts(55, 60)).toBe("down");
    expect(classifyAvailabilityCounts(55, 60, 90, 80)).toBe("up");
  });

  test("sums rather than averages: the bigger region weighs more", () => {
    // Region A: 60 probes, all up. Region B: 1 probe, down.
    // Summed  → 60/61 = 98.4% (degraded, one failure).
    // Averaged → (100 + 0) / 2 = 50% (down). Only one of these can be right,
    // and the server's rule is the sum.
    expect(classifyAvailabilityCounts(60, 61)).toBe("degraded");
  });
});

describe("availabilityFill", () => {
  test("gives no-data the muted treatment and everything else a solid colour", () => {
    expect(availabilityFill("noData").opacity).toBeLessThan(1);
    expect(availabilityFill(undefined).opacity).toBeLessThan(1);
    for (const status of ["up", "degraded", "down"] as const) {
      expect(availabilityFill(status).opacity).toBe(1);
    }
    const fills = (["up", "degraded", "down", "noData"] as const).map(
      (s) => availabilityFill(s).fill,
    );
    expect(new Set(fills).size).toBe(4);
  });
});

describe("formatAvailabilityPct", () => {
  test("keeps a near-perfect number distinguishable from a perfect one", () => {
    expect(formatAvailabilityPct(100)).toBe("100%");
    expect(formatAvailabilityPct(99.95)).toBe("99.95%");
    expect(formatAvailabilityPct(90)).toBe("90.0%");
  });

  test("returns null for absent data rather than a fabricated number", () => {
    expect(formatAvailabilityPct(undefined)).toBeNull();
    expect(formatAvailabilityPct(Number.NaN)).toBeNull();
  });
});
