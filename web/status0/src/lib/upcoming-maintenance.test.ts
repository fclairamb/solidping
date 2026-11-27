import { describe, test, expect } from "bun:test";
import { maintenanceHint } from "./upcoming-maintenance";

const now = Date.parse("2026-11-27T10:00:00Z");

describe("maintenanceHint", () => {
  test("active window is in progress", () => {
    expect(maintenanceHint(true, "2026-11-27T09:00:00Z", now)).toEqual({
      key: "maintenanceInProgress",
    });
  });
  test("minutes, hours, days", () => {
    expect(maintenanceHint(false, "2026-11-27T10:30:00Z", now)).toEqual({
      key: "maintenanceStartsInMinutes",
      count: 30,
    });
    expect(maintenanceHint(false, "2026-11-27T12:00:00Z", now)).toEqual({
      key: "maintenanceStartsInHours",
      count: 2,
    });
    expect(maintenanceHint(false, "2026-12-01T10:00:00Z", now)).toEqual({
      key: "maintenanceStartsInDays",
      count: 4,
    });
  });
});
