import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";

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
  const now = new Date("2026-08-14T12:00:00Z"); // 2026-08-14 14:00 local

  // Pinned to a fixed, non-UTC, positive offset (Europe/Paris = UTC+2 in
  // August) so the local-vs-UTC assertions below are deterministic
  // regardless of the CI runner's own timezone. Restored after this describe
  // block so it can't leak into other tests in the file.
  beforeAll(() => {
    vi.stubEnv("TZ", "Europe/Paris");
  });
  afterAll(() => {
    vi.unstubAllEnvs();
  });

  // Computed the same way the function under test derives its marker, so
  // this test doesn't hardcode a specific abbreviation (which varies by
  // ICU/runtime — e.g. "CEST" vs "GMT+2") while still pinning the timezone.
  function localZoneMarker(date: Date): string {
    const parts = new Intl.DateTimeFormat(undefined, { timeZoneName: "short" }).formatToParts(
      date,
    );
    return parts.find((p) => p.type === "timeZoneName")?.value ?? "";
  }

  it("renders the local clock + zone marker, no date prefix, when the date is the same LOCAL day as now", () => {
    // 2026-08-14T09:31:07Z is 2026-08-14 11:31:07 local (UTC+2); `now` is
    // also local-Aug-14, so this is "today".
    const date = new Date("2026-08-14T09:31:07Z");
    expect(formatInlineAbsolute(date, now)).toBe(`11:31:07 ${localZoneMarker(date)}`);
  });

  it("prefixes the ISO-ordered local date when the date is a different LOCAL day than now", () => {
    const old = new Date("2026-08-10T09:31:07Z"); // 2026-08-10 11:31:07 local
    expect(formatInlineAbsolute(old, now)).toBe(
      `2026-08-10 11:31:07 ${localZoneMarker(old)}`,
    );
  });

  it("follows the LOCAL day, not the UTC day, at the midnight boundary (positive control)", () => {
    // 2026-08-14T22:30:00Z is UTC-day Aug 14, but at UTC+2 it is already
    // 2026-08-15 00:30:00 local — a different LOCAL day than its own UTC
    // day. `now` below is local-Aug-15 too, so this must render as "today"
    // (local clock, no date prefix) even though the UTC day differs from
    // the local day. Against the OLD implementation (UTC clock gated by a
    // local-day "isToday") this would have wrongly rendered the bare UTC
    // clock "22:30:00 UTC" with no date — this assertion fails against that
    // implementation, which is the point of a positive control.
    const boundary = new Date("2026-08-14T22:30:00Z");
    const localToday = new Date("2026-08-15T08:00:00Z"); // 2026-08-15 10:00 local
    expect(formatInlineAbsolute(boundary, localToday)).toBe(
      `00:30:00 ${localZoneMarker(boundary)}`,
    );
  });

  it("falls back to a UTC-offset label when Intl can't produce a zone name", () => {
    const spy = vi.spyOn(Intl, "DateTimeFormat").mockImplementation(() => {
      throw new Error("Intl unavailable");
    });
    try {
      const date = new Date("2026-08-14T09:31:07Z");
      expect(formatInlineAbsolute(date, now)).toBe("11:31:07 UTC+2");
    } finally {
      spy.mockRestore();
    }
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
