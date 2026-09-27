import { describe, expect, it } from "vitest";

import { incidentKindOf, incidentKindTextClass } from "./incident-kind";

describe("incidentKindOf", () => {
  it("keeps the three known kinds", () => {
    expect(incidentKindOf("check")).toBe("check");
    expect(incidentKindOf("degraded")).toBe("degraded");
    expect(incidentKindOf("slo_burn")).toBe("slo_burn");
  });

  it("reads a missing or unknown kind as an outage", () => {
    expect(incidentKindOf(undefined)).toBe("check");
    expect(incidentKindOf(null)).toBe("check");
    expect(incidentKindOf("")).toBe("check");
    expect(incidentKindOf("maintenance")).toBe("check");
  });
});

describe("incidentKindTextClass", () => {
  it("gives each kind its own colour", () => {
    const classes = new Set(
      ["check", "degraded", "slo_burn"].map((k) => incidentKindTextClass(k)),
    );
    expect(classes.size).toBe(3);
    expect(incidentKindTextClass(undefined)).toBe(incidentKindTextClass("check"));
  });
});
