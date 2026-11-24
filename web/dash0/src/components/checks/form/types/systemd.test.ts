import { describe, expect, it } from "vitest";

import { systemdModule, systemdUnitError } from "./systemd";
import type { CheckConfig } from "./common";

function roundTrip(stored: CheckConfig): CheckConfig {
  return systemdModule.toConfig(systemdModule.fromConfig(stored)).config;
}

describe("systemdModule", () => {
  it("round-trips the default expected state", () => {
    const stored = { unit: "nginx" };
    expect(systemdModule.fromConfig(stored).expectedState).toBe("active");
    expect(roundTrip(stored)).toEqual(stored);
  });

  it("round-trips expected_state inactive", () => {
    const stored = { unit: "backup.timer", expected_state: "inactive" };
    expect(roundTrip(stored)).toEqual(stored);
  });

  it("requires a unit", () => {
    const { errors } = systemdModule.toConfig({
      unit: "",
      expectedState: "active",
    });
    expect(errors.map((e) => e.name)).toEqual(["unit"]);
  });

  it("mirrors the backend unit rule", () => {
    for (const unit of ["-H", "--host=x", "a b", "a;b", "a".repeat(300)]) {
      expect(systemdUnitError(unit)).toBe("systemdUnitInvalid");
    }
    for (const unit of ["nginx", "app@1.service", "foo.timer"]) {
      expect(systemdUnitError(unit)).toBeNull();
    }
  });
});
