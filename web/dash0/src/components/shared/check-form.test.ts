import { describe, expect, it } from "vitest";

import {
  buildIntervalOptions,
  formatPeriod,
  hmsToSeconds,
  parsePeriod,
  secondsToHMS,
} from "./check-form";

// Long-period (>24h) support: spec "Check edit page: docs link is generic,
// domain expiration has no warning/critical days, and periods stop at 24h"
// requires the HH:MM:SS helpers to survive 3-digit hour values (168h/336h/
// 720h) without truncation.
describe("HMS helpers — periods beyond 24h", () => {
  it("hmsToSeconds parses 3-digit hour values", () => {
    expect(hmsToSeconds("168:00:00")).toBe(604800); // 1 week
    expect(hmsToSeconds("336:00:00")).toBe(1209600); // 2 weeks
    expect(hmsToSeconds("720:00:00")).toBe(2592000); // 30 days
  });

  it("secondsToHMS emits 3-digit hour values without truncation", () => {
    expect(secondsToHMS(604800)).toBe("168:00:00");
    expect(secondsToHMS(1209600)).toBe("336:00:00");
    expect(secondsToHMS(2592000)).toBe("720:00:00");
  });

  it("round-trips a 2-week period through secondsToHMS and hmsToSeconds", () => {
    const seconds = 1209600;
    expect(hmsToSeconds(secondsToHMS(seconds))).toBe(seconds);
  });

  it("formatPeriod renders a 2-week value as 336:00:00", () => {
    expect(formatPeriod(2, "weeks")).toBe("336:00:00");
  });

  it("parsePeriod recognizes a whole-week period as weeks, not days/hours", () => {
    expect(parsePeriod("336:00:00")).toEqual({ value: 2, unit: "weeks" });
    expect(parsePeriod("720:00:00")).toEqual({ value: 30, unit: "days" });
  });
});

describe("buildIntervalOptions — long-horizon entries", () => {
  it("includes 1 week / 2 weeks / 30 days when uncapped (maxSeconds = 0)", () => {
    const options = buildIntervalOptions(0, 0);
    expect(options).toContainEqual({ value: "168:00:00", label: "1 week" });
    expect(options).toContainEqual({ value: "336:00:00", label: "2 weeks" });
    expect(options).toContainEqual({ value: "720:00:00", label: "30 days" });
  });

  it("excludes long-horizon entries once maxSeconds caps below them", () => {
    // e.g. a hypothetical type capped at 24h (86400s).
    const options = buildIntervalOptions(0, 86400);
    expect(options.some((o) => o.value === "168:00:00")).toBe(false);
    expect(options[options.length - 1]).toEqual({ value: "24:00:00", label: "24 hours" });
  });

  it("respects the type's MinPeriod floor (e.g. domain's 6h)", () => {
    const options = buildIntervalOptions(6 * 3600, 0);
    expect(options[0]).toEqual({ value: "06:00:00", label: "6 hours" });
    // Long options still surface since domain has no MaxPeriod.
    expect(options).toContainEqual({ value: "720:00:00", label: "30 days" });
  });
});
