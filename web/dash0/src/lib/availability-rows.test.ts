import { describe, expect, it } from "vitest";
import type { CheckAvailabilityPeriod } from "@/api/hooks";

import {
  formatAvailabilityPct,
  formatDurationSeconds,
  mapAvailabilityRow,
} from "@/lib/availability-rows";

// A fully-populated server period; tests override the fields they care about.
function period(
  overrides: Partial<CheckAvailabilityPeriod> = {},
): CheckAvailabilityPeriod {
  return {
    period: "7d",
    windowStart: "2026-06-23T00:00:00Z",
    windowEnd: "2026-06-30T00:00:00Z",
    monitoredSeconds: 7 * 86_400,
    partial: false,
    hasData: true,
    totalChecks: 100,
    successfulChecks: 90,
    availabilityPct: 90,
    downtimeSeconds: 60_480,
    incidents: { count: 0 },
    ...overrides,
  };
}

describe("formatAvailabilityPct", () => {
  it("shows 100% without decimals, two decimals near-perfect, one otherwise", () => {
    expect(formatAvailabilityPct(100)).toBe("100%");
    expect(formatAvailabilityPct(99.107)).toBe("99.11%");
    expect(formatAvailabilityPct(90)).toBe("90.0%");
  });
});

describe("formatDurationSeconds", () => {
  it("formats seconds into the largest two units", () => {
    expect(formatDurationSeconds(0)).toBe("0s");
    expect(formatDurationSeconds(45)).toBe("45s");
    expect(formatDurationSeconds(90)).toBe("1m 30s");
    expect(formatDurationSeconds(3_600)).toBe("1h 0m");
    expect(formatDurationSeconds(90_000)).toBe("1d 1h");
  });
});

describe("mapAvailabilityRow", () => {
  it("renders '-' for a missing period (no data is not 100%)", () => {
    const v = mapAvailabilityRow(undefined);
    expect(v.availabilityText).toBe("-");
    expect(v.downtimeText).toBe("-");
    expect(v.incidentCount).toBe(0);
    expect(v.longestText).toBeNull();
    expect(v.averageText).toBeNull();
    expect(v.monitoredDays).toBeNull();
  });

  it("renders '-' when the period has hasData=false", () => {
    const v = mapAvailabilityRow(
      period({ hasData: false, availabilityPct: null, downtimeSeconds: 0 }),
    );
    expect(v.availabilityText).toBe("-");
    expect(v.downtimeText).toBe("-");
  });

  it("maps probe-ratio availability and probe-time downtime", () => {
    const v = mapAvailabilityRow(period());
    expect(v.availabilityText).toBe("90.0%");
    expect(v.downtimeText).toBe("16h 48m"); // 60480s
  });

  it("maps the incident block when there are incidents", () => {
    const v = mapAvailabilityRow(
      period({
        incidents: {
          count: 2,
          longestSeconds: 9_000,
          averageSeconds: 6_300,
          totalDowntimeSeconds: 12_600,
        },
      }),
    );
    expect(v.incidentCount).toBe(2);
    expect(v.longestText).toBe("2h 30m");
    expect(v.averageText).toBe("1h 45m");
  });

  it("surfaces monitoredDays only on partial rows", () => {
    expect(mapAvailabilityRow(period({ partial: false })).monitoredDays).toBeNull();

    // A 40-day-old check asked over 365d: partial, ~40 days monitored.
    const v = mapAvailabilityRow(
      period({ partial: true, monitoredSeconds: 40 * 86_400 }),
    );
    expect(v.monitoredDays).toBe(40);

    // Never reports 0 days for a partial row with a sub-day window.
    const young = mapAvailabilityRow(
      period({ partial: true, monitoredSeconds: 3_600 }),
    );
    expect(young.monitoredDays).toBe(1);
  });
});
