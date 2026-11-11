import { describe, expect, it } from "vitest";
import { REGION_KEY_ADVISORY_CUTOFF, regionKeyPredatesFix } from "./region-key-advisory";

describe("regionKeyPredatesFix", () => {
  it("flags keys created before the cutoff only", () => {
    expect(regionKeyPredatesFix("2026-11-05T10:00:00Z")).toBe(true);
    expect(regionKeyPredatesFix(REGION_KEY_ADVISORY_CUTOFF)).toBe(false);
    expect(regionKeyPredatesFix("2027-01-01T00:00:00Z")).toBe(false);
  });

  it("never flags a missing or unparsable date", () => {
    expect(regionKeyPredatesFix(undefined)).toBe(false);
    expect(regionKeyPredatesFix(null)).toBe(false);
    expect(regionKeyPredatesFix("")).toBe(false);
    expect(regionKeyPredatesFix("not a date")).toBe(false);
  });
});
