import { describe, expect, it } from "vitest";

import { availabilityTier } from "@/lib/availability-tier";

// Pins the badge-tier boundaries for the 24h availability KPI (spec
// 2026-08-26-09) so a threshold tweak can't silently drift from what's
// actually rendered. The headline regression this whole feature fixes: null
// must map to "noData", never be treated as a number and rendered as a
// fabricated 100%/"Operational".
describe("availabilityTier", () => {
  it("maps null to noData — never a fabricated percentage", () => {
    expect(availabilityTier(null)).toBe("noData");
  });

  it("is operational at and above the operational threshold", () => {
    expect(availabilityTier(100)).toBe("operational");
    expect(availabilityTier(99.9)).toBe("operational");
  });

  it("is degraded just under the operational threshold, down to the degraded threshold", () => {
    expect(availabilityTier(99.89)).toBe("degraded");
    expect(availabilityTier(99)).toBe("degraded");
  });

  it("is down below the degraded threshold", () => {
    expect(availabilityTier(98.99)).toBe("down");
    expect(availabilityTier(0)).toBe("down");
  });
});
