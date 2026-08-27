import { describe, it, expect } from "vitest";
import {
  AVAILABILITY_THRESHOLD_DEGRADED,
  AVAILABILITY_THRESHOLD_UP,
  availabilityCellClass,
  availabilityDotClass,
  classifyAvailability,
  formatAvailabilityPct,
  type AvailabilityStatus,
} from "./availability-status";

describe("classifyAvailability", () => {
  it("mirrors the server's thresholds", () => {
    // models.DefaultAvailabilityThreshold{Up,Degraded}.
    expect(AVAILABILITY_THRESHOLD_UP).toBe(99.9);
    expect(AVAILABILITY_THRESHOLD_DEGRADED).toBe(99.0);
  });

  it.each<[string, number, number | undefined, AvailabilityStatus]>([
    ["perfect", 100, 0, "up"],
    ["at the up threshold", 99.9, 1, "up"],
    ["under the up threshold", 99.5, 3, "degraded"],
    ["at the degraded threshold", 99.0, 5, "degraded"],
    ["well under with many failures", 50, 30, "down"],
    ["two failures may go red", 50, 2, "down"],
    ["a single failed sample stays amber", 50, 1, "degraded"],
    ["zero availability, one sample", 0, 1, "degraded"],
    ["zero availability, counts unknown", 0, undefined, "down"],
  ])("%s", (_name, pct, failures, want) => {
    expect(classifyAvailability(pct, failures)).toBe(want);
  });

  it("treats a missing percentage as no-data, never as 100%", () => {
    expect(classifyAvailability(null)).toBe("noData");
    expect(classifyAvailability(undefined)).toBe("noData");
    expect(classifyAvailability(Number.NaN)).toBe("noData");
    // The positive control: a real 100 IS up, so "noData" above is genuinely
    // about absence and not a blanket fallback.
    expect(classifyAvailability(100)).toBe("up");
  });

  it("skips the small-bucket guard when the failure count is unknown", () => {
    // undefined means "could not count", NOT zero. With zero the guard would
    // fire on every bucket and nothing could ever render red.
    expect(classifyAvailability(20, undefined)).toBe("down");
    expect(classifyAvailability(20, 0)).toBe("degraded");
  });
});

describe("colour mapping", () => {
  it("gives every status a distinct class, gray for no-data", () => {
    const statuses: AvailabilityStatus[] = [
      "up",
      "degraded",
      "down",
      "noData",
    ];
    const cells = statuses.map(availabilityCellClass);
    expect(new Set(cells).size).toBe(statuses.length);
    expect(availabilityCellClass("noData")).toContain("muted");
    expect(availabilityCellClass("up")).toContain("status-ok");
    expect(availabilityCellClass("degraded")).toContain("status-warning");
    expect(availabilityCellClass("down")).toContain("status-error");

    const dots = statuses.map(availabilityDotClass);
    expect(new Set(dots).size).toBe(statuses.length);
  });
});

describe("formatAvailabilityPct", () => {
  it("formats the same way the availability table below the chart does", () => {
    expect(formatAvailabilityPct(100)).toBe("100%");
    expect(formatAvailabilityPct(0)).toBe("0.0%");
    expect(formatAvailabilityPct(99.107)).toBe("99.11%");
    expect(formatAvailabilityPct(90)).toBe("90.0%");
  });

  it("keeps a near-perfect number distinguishable from a perfect one", () => {
    // 99.95% is not 100%: at one decimal it would render "100.0%" and the
    // difference would vanish, which is exactly the fabricated figure this spec
    // exists to remove. Only an EXACT 100 gets the bare "100%" — a value that
    // merely rounds there still carries its decimals.
    expect(formatAvailabilityPct(99.95)).toBe("99.95%");
    expect(formatAvailabilityPct(99.999)).toBe("100.00%");
    expect(formatAvailabilityPct(100)).toBe("100%");
  });

  it("returns null for no-data rather than a fabricated number", () => {
    expect(formatAvailabilityPct(null)).toBeNull();
    expect(formatAvailabilityPct(undefined)).toBeNull();
    expect(formatAvailabilityPct(Number.NaN)).toBeNull();
  });
});
