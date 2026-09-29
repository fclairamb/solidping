import { describe, expect, it } from "vitest";

import {
  incidentKindOf,
  incidentKindTextClass,
  incidentRowClass,
} from "./incident-kind";

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

describe("incidentRowClass", () => {
  it("tints an open row in its kind's colour", () => {
    expect(incidentRowClass("active", "check")).toContain("bg-red-500/5");
    expect(incidentRowClass("active", "degraded")).toContain("bg-amber-500/5");
    expect(incidentRowClass("active", "slo_burn")).toContain("bg-violet-500/5");

    const classes = new Set(
      ["check", "degraded", "slo_burn"].map((k) => incidentRowClass("active", k)),
    );
    expect(classes.size).toBe(3);
  });

  it("carries its own hover, so an open row never falls back to muted", () => {
    for (const kind of ["check", "degraded", "slo_burn"]) {
      const cls = incidentRowClass("active", kind);
      expect(cls).toContain("hover:");
      expect(cls).not.toContain("bg-muted");
      expect(cls).toContain("dark:");
    }
  });

  it("keeps a resolved row — or one with no state — on the card background", () => {
    expect(incidentRowClass("resolved", "check")).toBe("hover:bg-muted/40");
    expect(incidentRowClass(undefined, "check")).toBe("hover:bg-muted/40");
    expect(incidentRowClass(null, "degraded")).toBe("hover:bg-muted/40");
    expect(incidentRowClass("resolved", "degraded")).not.toMatch(
      /bg-(red|amber|violet)-/,
    );
  });

  it("reads a missing or unknown kind as an outage", () => {
    expect(incidentRowClass("active", undefined)).toBe(
      incidentRowClass("active", "check"),
    );
    expect(incidentRowClass("active", "maintenance")).toBe(
      incidentRowClass("active", "check"),
    );
  });
});
