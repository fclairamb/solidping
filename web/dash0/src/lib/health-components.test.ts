import { describe, expect, it } from "vitest";
import { componentTimelines, healthComponents } from "./health-components";

describe("healthComponents", () => {
  it("reads components and falls back to unknown for a bad status", () => {
    const rows = healthComponents({
      components: [
        { name: "db", status: "ok", summary: "12 ms" },
        { name: "disk", status: "failed", message: "full", label: "Disk" },
        { name: "odd", status: "weird" },
        { status: "ok" },
      ],
    });
    expect(rows.map((r) => [r.name, r.status])).toEqual([
      ["db", "ok"],
      ["disk", "failed"],
      ["odd", "unknown"],
    ]);
    expect(rows[1].message).toBe("full");
  });

  it("is empty without components", () => {
    expect(healthComponents(undefined)).toEqual([]);
    expect(healthComponents({ components: "x" })).toEqual([]);
  });
});

describe("componentTimelines", () => {
  it("orders statuses oldest to newest and marks missing components unknown", () => {
    const newest = { components: [{ name: "db", status: "failed" }, { name: "cache", status: "ok" }] };
    const oldest = { components: [{ name: "db", status: "ok" }] };
    const timelines = componentTimelines([newest, oldest]);
    expect(timelines).toEqual([
      { name: "db", statuses: ["ok", "failed"] },
      { name: "cache", statuses: ["unknown", "ok"] },
    ]);
  });
});
