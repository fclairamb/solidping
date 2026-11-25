import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  lastPlacementSeed,
  loadLastPlacement,
  parseLastPlacement,
  pruneLastPlacement,
  saveLastPlacement,
} from "./last-placement";

beforeEach(() => {
  const store = new Map<string, string>();
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  });
});

describe("last placement (solidping-business#113)", () => {
  it("remembers a pinned placement per org", () => {
    saveLastPlacement("acme", { placement: "pinned", regions: ["eu1", "us1"] });

    expect(loadLastPlacement("acme")).toEqual({ placement: "pinned", regions: ["eu1", "us1"] });
    expect(loadLastPlacement("other")).toBeNull();
  });

  it("remembers an automatic placement with its count, without regions", () => {
    saveLastPlacement("acme", { placement: "auto", regionCount: 3, regions: ["eu1"] });

    expect(loadLastPlacement("acme")).toEqual({ placement: "auto", regions: [], regionCount: 3 });
  });

  it("keeps the stored placement when a submission carried none", () => {
    saveLastPlacement("acme", { placement: "pinned", regions: ["eu1"] });
    saveLastPlacement("acme", {});
    saveLastPlacement("acme", { placement: "pinned", regions: [] });

    expect(loadLastPlacement("acme")).toEqual({ placement: "pinned", regions: ["eu1"] });
  });

  it("ignores corrupt or foreign values", () => {
    localStorage.setItem("solidping_last_placement:acme", "{not json");
    expect(loadLastPlacement("acme")).toBeNull();
    expect(parseLastPlacement({ placement: "everywhere" })).toBeNull();
    expect(parseLastPlacement({ placement: "pinned", regions: [1, ""] })).toBeNull();
    expect(parseLastPlacement({ placement: "auto", regionCount: -2 })).toEqual({ placement: "auto", regions: [] });
  });

  it("seeds only a create whose regions were not prefilled", () => {
    const last = { placement: "pinned" as const, regions: ["eu1"] };

    expect(lastPlacementSeed("create", undefined, last)).toEqual(last);
    expect(lastPlacementSeed("create", [], last)).toEqual(last);
    expect(lastPlacementSeed("create", ["us1"], last)).toBeNull();
    expect(lastPlacementSeed("edit", undefined, last)).toBeNull();
    expect(lastPlacementSeed("create", undefined, null)).toBeNull();
  });

  it("drops regions the org no longer offers, falling back to automatic", () => {
    const last = { placement: "pinned" as const, regions: ["eu1", "gone"] };

    expect(pruneLastPlacement(last, ["eu1", "us1"])).toEqual({ placement: "pinned", regions: ["eu1"] });
    expect(pruneLastPlacement(last, ["us1"])).toEqual({ placement: "auto", regions: [] });
  });
});
