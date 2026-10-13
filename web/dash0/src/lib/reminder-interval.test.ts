import { describe, expect, it } from "vitest";
import {
  REMINDER_INHERIT,
  isValidReminderInterval,
  reminderDurationLabel,
  reminderOptions,
  reminderRequestValue,
  reminderSelectValue,
} from "./reminder-interval";

describe("reminder interval", () => {
  it("labels durations compactly", () => {
    expect(reminderDurationLabel(0)).toBeNull();
    expect(reminderDurationLabel(900)).toBe("15 min");
    expect(reminderDurationLabel(3600)).toBe("1 h");
    expect(reminderDurationLabel(5400)).toBe("1 h 30 min");
    expect(reminderDurationLabel(86400)).toBe("24 h");
  });

  it("maps null to inherit and back", () => {
    expect(reminderSelectValue(null)).toBe(REMINDER_INHERIT);
    expect(reminderSelectValue(undefined)).toBe(REMINDER_INHERIT);
    expect(reminderSelectValue(0)).toBe("0");
    expect(reminderRequestValue(REMINDER_INHERIT)).toBeNull();
    expect(reminderRequestValue("1800")).toBe(1800);
    expect(reminderRequestValue("0")).toBe(0);
  });

  it("accepts 0 and 15 min to 24 h only", () => {
    expect(isValidReminderInterval(0)).toBe(true);
    expect(isValidReminderInterval(900)).toBe(true);
    expect(isValidReminderInterval(86400)).toBe(true);
    expect(isValidReminderInterval(-1)).toBe(false);
    expect(isValidReminderInterval(899)).toBe(false);
    expect(isValidReminderInterval(86401)).toBe(false);
  });

  it("keeps a stored value the presets do not list", () => {
    expect(reminderOptions(null)).toEqual([0, 900, 1800, 3600, 7200, 14400, 86400]);
    expect(reminderOptions(5400)).toContain(5400);
    expect(reminderOptions(5400).indexOf(5400)).toBe(4);
    expect(reminderOptions(3600)).toHaveLength(7);
  });
});
