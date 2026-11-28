import { describe, expect, it } from "vitest";

import { hostDiskPathError, hostModule, hostPercentError } from "./host";
import type { CheckConfig } from "./common";

function roundTrip(stored: CheckConfig): CheckConfig {
  return hostModule.toConfig(hostModule.fromConfig(stored)).config;
}

describe("hostModule", () => {
  it("round-trips an empty config (every field optional)", () => {
    expect(roundTrip({})).toEqual({});
    expect(hostModule.toConfig(hostModule.fromConfig({})).errors).toEqual([]);
  });

  it("round-trips the path and thresholds", () => {
    const stored = {
      diskPath: "/host",
      diskWarningPercent: 80,
      diskCriticalPercent: 90,
      cpuCriticalPercent: 95,
    };
    expect(roundTrip(stored)).toEqual(stored);
  });

  it("rejects a percent out of range", () => {
    const state = { ...hostModule.fromConfig({}), memoryCriticalPercent: "120" };
    const { errors } = hostModule.toConfig(state);
    expect(errors.map((e) => e.name)).toEqual(["memoryCriticalPercent"]);
    for (const bad of ["0", "-5", "100.5", "abc"]) {
      expect(hostPercentError(bad)).toBe("hostPercentInvalid");
    }
    for (const good of ["", "1", "100", "99.9"]) {
      expect(hostPercentError(good)).toBeNull();
    }
  });

  it("rejects warning >= critical", () => {
    const state = {
      ...hostModule.fromConfig({}),
      cpuWarningPercent: "90",
      cpuCriticalPercent: "90",
    };
    const { errors } = hostModule.toConfig(state);
    expect(errors.map((e) => e.name)).toEqual(["cpuWarningPercent"]);
  });

  it("mirrors the disk path rule", () => {
    for (const bad of ["data", "relative/path"]) {
      expect(hostDiskPathError(bad)).toBe("hostDiskPathInvalid");
    }
    for (const good of ["", "/", "/var/lib", "D:\\", "C:/data"]) {
      expect(hostDiskPathError(good)).toBeNull();
    }
  });
});
