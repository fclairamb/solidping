import { describe, expect, it } from "vitest";

import type { MaintenanceWindow } from "@/api/hooks";
import {
  nextOccurrences,
} from "@/lib/maintenance-window-schedule";
import {
  isActiveAt,
  occurrenceStartUTC,
} from "@/lib/maintenance-window-status";

// Parity vectors mirrored from
// server/internal/db/models/maintenance_window_test.go.
//
// The client recurrence math (occurrenceStartUTC / nextOccurrences / isActiveAt)
// MUST produce occurrence lists identical to the backend's known-good vectors so
// that the dashboard preview agrees with the server-canonical NextOccurrences.
// Every constant below is the SAME anchor instant the Go test uses, expressed as
// the equivalent UTC ISO-8601 string, so the vectors are genuinely shared.

// utc builds the UTC ISO instant for a wall-clock UTC date/time. This is the
// TS equivalent of the Go helper `ts(y, m, d, h, min)` which uses time.UTC.
// month is 1-based here to read like the Go time.Month constants.
function utc(
  y: number,
  month: number,
  d: number,
  h: number,
  min: number,
): string {
  return new Date(Date.UTC(y, month - 1, d, h, min, 0, 0)).toISOString();
}

// win is the TS analogue of the Go `mw(...)` constructor — only the recurrence
// fields matter to the math. The rest are filler to satisfy the type.
function win(
  startAt: string,
  endAt: string,
  recurrence: MaintenanceWindow["recurrence"],
  recurrenceEnd?: string,
): MaintenanceWindow {
  return {
    uid: "test",
    title: "test",
    startAt,
    endAt,
    recurrence,
    recurrenceEnd,
    createdAt: startAt,
    updatedAt: startAt,
  };
}

// Convenience: the UTC (month, day) pair of an occurrence's start, for asserting
// no-drift clamping the same way the Go test compares Month()/Day().
function monthDay(iso: string): { month: number; day: number } {
  const d = new Date(iso);
  return { month: d.getUTCMonth() + 1, day: d.getUTCDate() };
}

describe("occurrenceStartUTC — addMonths clamping parity (TestAddMonthsClampsDay)", () => {
  // Anchor: Jan 31 2026 09:00. Mirrors `jan31 := ts(2026, time.January, 31, 9, 0)`.
  const jan31 = new Date(utc(2026, 1, 31, 9, 0));

  it("+1mo lands on Feb 28 (non-leap), NOT Mar 3", () => {
    const feb = occurrenceStartUTC(jan31, "monthly", 1);
    expect(feb.getUTCFullYear()).toBe(2026);
    expect(feb.getUTCMonth() + 1).toBe(2); // February
    expect(feb.getUTCDate()).toBe(28);
    expect(feb.getUTCHours()).toBe(9);
  });

  it("+2mo lands on Mar 31 — anchored on the original, no accumulated drift", () => {
    const mar = occurrenceStartUTC(jan31, "monthly", 2);
    expect(mar.getUTCMonth() + 1).toBe(3); // March
    expect(mar.getUTCDate()).toBe(31);
  });

  it("+3mo lands on Apr 30", () => {
    const apr = occurrenceStartUTC(jan31, "monthly", 3);
    expect(apr.getUTCMonth() + 1).toBe(4); // April
    expect(apr.getUTCDate()).toBe(30);
  });

  it("leap year: Jan 31 +1mo -> Feb 29", () => {
    const leap = occurrenceStartUTC(new Date(utc(2024, 1, 31, 9, 0)), "monthly", 1);
    expect(leap.getUTCMonth() + 1).toBe(2); // February
    expect(leap.getUTCDate()).toBe(29);
  });
});

describe("nextOccurrences — none (TestNextOccurrences_None)", () => {
  const start = utc(2026, 6, 20, 12, 0);
  const end = utc(2026, 6, 20, 13, 0);
  const w = win(start, end, "none");

  it("upcoming one-off: single occurrence returned", () => {
    const occ = nextOccurrences(w, new Date(utc(2026, 6, 15, 0, 0)), 3);
    expect(occ).toEqual([{ startAt: start, endAt: end }]);
  });

  it("active one-off (from inside the window): still returned", () => {
    const occ = nextOccurrences(w, new Date(utc(2026, 6, 20, 12, 30)), 3);
    expect(occ).toHaveLength(1);
  });

  it("past one-off: empty", () => {
    const occ = nextOccurrences(w, new Date(utc(2026, 6, 21, 0, 0)), 3);
    expect(occ).toHaveLength(0);
  });

  it("n <= 0 returns empty", () => {
    expect(nextOccurrences(w, new Date(utc(2026, 6, 15, 0, 0)), 0)).toEqual([]);
  });
});

describe("nextOccurrences — daily chronological & active-first (TestNextOccurrences_DailyChronologicalAndActiveFirst)", () => {
  // Anchor: Jun 1 2026 22:00–23:00, daily, no recurrence end.
  const start = utc(2026, 6, 1, 22, 0);
  const end = utc(2026, 6, 1, 23, 0);
  const w = win(start, end, "daily");

  it("from inside the Jun 10 slot puts that occurrence first, then chronological", () => {
    const from = new Date(utc(2026, 6, 10, 22, 30));
    const occ = nextOccurrences(w, from, 3);
    expect(occ.map((o) => o.startAt)).toEqual([
      utc(2026, 6, 10, 22, 0),
      utc(2026, 6, 11, 22, 0),
      utc(2026, 6, 12, 22, 0),
    ]);

    // Each lasts exactly one hour, and starts strictly increase.
    for (let i = 0; i < occ.length; i++) {
      expect(new Date(occ[i].endAt).getTime() - new Date(occ[i].startAt).getTime()).toBe(
        60 * 60 * 1000,
      );
      if (i > 0) {
        expect(new Date(occ[i].startAt).getTime()).toBeGreaterThan(
          new Date(occ[i - 1].startAt).getTime(),
        );
      }
    }
  });
});

describe("nextOccurrences — honors recurrenceEnd (TestNextOccurrences_HonorsRecurrenceEnd)", () => {
  // Daily Jun 1 22:00, recurrence end Jun 3 00:00 → only Jun 1 & Jun 2 occur.
  const w = win(
    utc(2026, 6, 1, 22, 0),
    utc(2026, 6, 1, 23, 0),
    "daily",
    utc(2026, 6, 3, 0, 0),
  );

  it("returns only the occurrences before the recurrence end", () => {
    const occ = nextOccurrences(w, new Date(utc(2026, 6, 1, 0, 0)), 10);
    expect(occ.map((o) => o.startAt)).toEqual([
      utc(2026, 6, 1, 22, 0),
      utc(2026, 6, 2, 22, 0),
    ]);
  });
});

describe("nextOccurrences — monthly clamp holds across the set (TestNextOccurrences_MonthlyClampHolds)", () => {
  // Anchor on the 31st: occurrences land on the last day of each short month,
  // with NO accumulated drift (Jan 31 → Feb 28 → Mar 31 → Apr 30).
  const w = win(utc(2026, 1, 31, 22, 0), utc(2026, 1, 31, 23, 0), "monthly");

  it("clamps month-end without drifting", () => {
    const occ = nextOccurrences(w, new Date(utc(2026, 1, 1, 0, 0)), 4);
    expect(occ.map((o) => monthDay(o.startAt))).toEqual([
      { month: 1, day: 31 }, // January
      { month: 2, day: 28 }, // February (clamped, NOT Mar 3)
      { month: 3, day: 31 }, // March (no drift)
      { month: 4, day: 30 }, // April
    ]);
  });
});

describe("isActiveAt — daily (TestIsActiveAt_Daily)", () => {
  const w = win(
    utc(2026, 6, 1, 22, 0),
    utc(2026, 6, 1, 23, 0),
    "daily",
    utc(2026, 6, 30, 0, 0),
  );

  const cases: { name: string; at: string; want: boolean }[] = [
    { name: "day 0 in slot", at: utc(2026, 6, 1, 22, 30), want: true },
    { name: "day 0 out of slot", at: utc(2026, 6, 1, 21, 30), want: false },
    { name: "day 10 in slot", at: utc(2026, 6, 11, 22, 30), want: true },
    { name: "day 10 between slots", at: utc(2026, 6, 11, 12, 0), want: false },
    { name: "before start", at: utc(2026, 5, 31, 22, 30), want: false },
    { name: "after recurrence end", at: utc(2026, 7, 1, 22, 30), want: false },
  ];

  for (const c of cases) {
    it(c.name, () => {
      expect(isActiveAt(w, new Date(c.at))).toBe(c.want);
    });
  }
});

describe("isActiveAt — weekly (TestIsActiveAt_Weekly)", () => {
  // 2026-06-01 is a Monday; weekly anchor on Monday 09:00–10:00.
  const w = win(utc(2026, 6, 1, 9, 0), utc(2026, 6, 1, 10, 0), "weekly");

  const cases: { name: string; at: string; want: boolean }[] = [
    { name: "anchor monday in slot", at: utc(2026, 6, 1, 9, 30), want: true },
    { name: "next monday in slot", at: utc(2026, 6, 8, 9, 30), want: true },
    { name: "four weeks later", at: utc(2026, 6, 29, 9, 30), want: true },
    { name: "tuesday not matched", at: utc(2026, 6, 2, 9, 30), want: false },
    { name: "monday out of slot", at: utc(2026, 6, 8, 11, 0), want: false },
  ];

  for (const c of cases) {
    it(c.name, () => {
      expect(isActiveAt(w, new Date(c.at))).toBe(c.want);
    });
  }
});

describe("isActiveAt — monthly no-drift (TestIsActiveAt_MonthlyNoDrift)", () => {
  const w = win(utc(2026, 1, 31, 22, 0), utc(2026, 1, 31, 23, 0), "monthly");

  const cases: { name: string; at: string; want: boolean }[] = [
    { name: "jan 31 active", at: utc(2026, 1, 31, 22, 30), want: true },
    { name: "feb 28 active (clamped)", at: utc(2026, 2, 28, 22, 30), want: true },
    { name: "feb 27 inactive", at: utc(2026, 2, 27, 22, 30), want: false },
    { name: "mar 31 active (no drift to mar 3)", at: utc(2026, 3, 31, 22, 30), want: true },
    { name: "mar 3 inactive (the old bug)", at: utc(2026, 3, 3, 22, 30), want: false },
    { name: "apr 30 active", at: utc(2026, 4, 30, 22, 30), want: true },
    { name: "may 31 active", at: utc(2026, 5, 31, 22, 30), want: true },
  ];

  for (const c of cases) {
    it(c.name, () => {
      expect(isActiveAt(w, new Date(c.at))).toBe(c.want);
    });
  }
});

describe("isActiveAt — monthly Jan 30 across leap (TestIsActiveAt_MonthlyJan30AcrossLeap)", () => {
  it("leap year 2024: Jan 30 -> Feb 29", () => {
    const leap = win(utc(2024, 1, 30, 10, 0), utc(2024, 1, 30, 11, 0), "monthly");
    expect(isActiveAt(leap, new Date(utc(2024, 2, 29, 10, 30)))).toBe(true);
    expect(isActiveAt(leap, new Date(utc(2024, 2, 28, 10, 30)))).toBe(false);
  });

  it("non-leap year 2026: Jan 30 -> Feb 28", () => {
    const nonLeap = win(utc(2026, 1, 30, 10, 0), utc(2026, 1, 30, 11, 0), "monthly");
    expect(isActiveAt(nonLeap, new Date(utc(2026, 2, 28, 10, 30)))).toBe(true);
  });
});
