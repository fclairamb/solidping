import { describe, expect, it } from "vitest";

import {
  failQuorumMode,
  failQuorumValue,
  isFailingRegionStatus,
  resolveFailQuorum,
} from "@/lib/fail-quorum";

describe("resolveFailQuorum", () => {
  it("defaults to all regions for 1-2 and a majority for 3+", () => {
    expect(resolveFailQuorum(undefined, 0)).toBe(0);
    expect(resolveFailQuorum(undefined, 1)).toBe(1);
    expect(resolveFailQuorum("default", 2)).toBe(2);
    expect(resolveFailQuorum("default", 3)).toBe(2);
    expect(resolveFailQuorum("default", 4)).toBe(3);
    expect(resolveFailQuorum("default", 5)).toBe(3);
  });

  it("resolves all, majority and a clamped count", () => {
    expect(resolveFailQuorum("all", 3)).toBe(3);
    expect(resolveFailQuorum("majority", 2)).toBe(2);
    expect(resolveFailQuorum("majority", 4)).toBe(3);
    expect(resolveFailQuorum(2, 4)).toBe(2);
    expect(resolveFailQuorum(7, 3)).toBe(3);
  });
});

describe("failQuorumMode / failQuorumValue", () => {
  it("round-trips every form", () => {
    expect(failQuorumMode(undefined)).toEqual({ mode: "default", count: "" });
    expect(failQuorumMode("default")).toEqual({ mode: "default", count: "" });
    expect(failQuorumMode("majority")).toEqual({ mode: "majority", count: "" });
    expect(failQuorumMode(2)).toEqual({ mode: "count", count: "2" });

    expect(failQuorumValue("default", "")).toBe("default");
    expect(failQuorumValue("all", "9")).toBe("all");
    expect(failQuorumValue("count", "2")).toBe(2);
  });

  it("refuses a count that is not a whole number in range", () => {
    expect(failQuorumValue("count", "")).toBeUndefined();
    expect(failQuorumValue("count", "0")).toBeUndefined();
    expect(failQuorumValue("count", "2.5")).toBeUndefined();
    expect(failQuorumValue("count", "101")).toBeUndefined();
  });
});

describe("isFailingRegionStatus", () => {
  it("counts down, timeout and error only", () => {
    expect(isFailingRegionStatus("down")).toBe(true);
    expect(isFailingRegionStatus("timeout")).toBe(true);
    expect(isFailingRegionStatus("error")).toBe(true);
    expect(isFailingRegionStatus("warning")).toBe(false);
    expect(isFailingRegionStatus("up")).toBe(false);
    expect(isFailingRegionStatus(undefined)).toBe(false);
  });
});
