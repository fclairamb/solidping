import { describe, expect, it } from "vitest";

import {
  formatInlineAbsolute,
  formatLocalDateTime,
  formatRelativeTime,
  formatTooltipText,
  formatUtcIso,
} from "@/lib/time-ago";

describe("formatRelativeTime", () => {
  const now = new Date("2026-08-14T12:00:00Z");

  it("renders 'just now' for the current instant", () => {
    expect(formatRelativeTime(now, now)).toBe("just now");
  });

  it("renders minutes", () => {
    expect(
      formatRelativeTime(new Date("2026-08-14T11:46:00Z"), now),
    ).toBe("14m ago");
  });

  it("renders hours", () => {
    expect(
      formatRelativeTime(new Date("2026-08-14T09:00:00Z"), now),
    ).toBe("3h ago");
  });

  it("renders days", () => {
    expect(
      formatRelativeTime(new Date("2026-08-10T12:00:00Z"), now),
    ).toBe("4d ago");
  });

  it("falls back to a locale date past 30 days", () => {
    const old = new Date("2026-06-01T12:00:00Z");
    expect(formatRelativeTime(old, now)).toBe(old.toLocaleDateString());
  });
});

describe("formatUtcIso", () => {
  it("renders ISO 8601 UTC without milliseconds", () => {
    expect(formatUtcIso(new Date("2026-08-14T09:31:07.123Z"))).toBe(
      "2026-08-14T09:31:07Z",
    );
  });
});

describe("formatLocalDateTime", () => {
  it("renders year-month-day ordered local time regardless of locale", () => {
    const d = new Date(2026, 7, 14, 11, 31, 7); // local: 2026-08-14 11:31:07
    expect(formatLocalDateTime(d)).toBe("2026-08-14 11:31:07");
  });

  it("zero-pads single-digit components", () => {
    const d = new Date(2026, 0, 5, 3, 4, 5); // local: 2026-01-05 03:04:05
    expect(formatLocalDateTime(d)).toBe("2026-01-05 03:04:05");
  });
});

describe("formatInlineAbsolute", () => {
  const now = new Date("2026-08-14T12:00:00Z");

  it("renders bare UTC time when the date is today", () => {
    expect(
      formatInlineAbsolute(new Date("2026-08-14T09:31:07Z"), now),
    ).toBe("09:31:07 UTC");
  });

  it("prefixes a locale date when the date is not today", () => {
    const old = new Date("2026-08-10T09:31:07Z");
    expect(formatInlineAbsolute(old, now)).toBe(
      `${old.toLocaleDateString()} 09:31:07 UTC`,
    );
  });
});

describe("formatTooltipText", () => {
  it("joins local and UTC representations", () => {
    const d = new Date("2026-08-14T09:31:07Z");
    expect(formatTooltipText(d)).toBe(
      `${formatLocalDateTime(d)} (local) · ${formatUtcIso(d)}`,
    );
  });
});
