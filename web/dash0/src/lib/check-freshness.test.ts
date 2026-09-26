import { describe, expect, it } from "vitest";

import {
  bucketCoveragePct,
  formatClockTime,
  lastRealResultAt,
  probesPerSecond,
  regionsDisagree,
  splitFreshness,
} from "@/lib/check-freshness";

describe("lastRealResultAt", () => {
  it("prefers the server's lastResultAt", () => {
    expect(
      lastRealResultAt({
        lastResultAt: "2026-09-24T13:41:00Z",
        lastResult: { status: "up", timestamp: "2026-09-24T21:00:00Z" },
      }),
    ).toBe("2026-09-24T13:41:00Z");
  });

  it("never reads an abandoned or lifecycle row as 'last checked'", () => {
    expect(
      lastRealResultAt({ lastResult: { status: "abandoned", timestamp: "2026-09-24T21:00:00Z" } }),
    ).toBeUndefined();
    expect(
      lastRealResultAt({ lastResult: { status: "created", timestamp: "2026-09-24T21:00:00Z" } }),
    ).toBeUndefined();
    expect(
      lastRealResultAt({ lastResult: { status: "down", timestamp: "2026-09-24T21:00:00Z" } }),
    ).toBe("2026-09-24T21:00:00Z");
  });
});

describe("region freshness", () => {
  const freshness = [
    { region: "eu-west", lastResultAt: "2026-09-24T21:40:00Z", stale: false },
    { region: "lauterbourg", lastResultAt: "2026-09-24T13:41:00Z", stale: true },
    { region: "us-east", lastResultAt: "2026-09-24T21:40:00Z", stale: false },
  ];

  it("splits silent regions from reporting ones", () => {
    const { silent, reporting } = splitFreshness(freshness);
    expect(silent.map((r) => r.region)).toEqual(["lauterbourg"]);
    expect(reporting).toHaveLength(2);
  });

  it("flags disagreement only when some regions report and others do not", () => {
    expect(regionsDisagree(freshness)).toBe(true);
    expect(regionsDisagree(freshness.filter((r) => !r.stale))).toBe(false);
    expect(regionsDisagree(freshness.filter((r) => r.stale))).toBe(false);
    expect(regionsDisagree(undefined)).toBe(false);
  });
});

describe("formatClockTime", () => {
  it("shows only the time for today and adds the day otherwise", () => {
    const now = new Date(2026, 8, 24, 22, 0);
    const today = new Date(2026, 8, 24, 13, 41).toISOString();
    const yesterday = new Date(2026, 8, 23, 13, 41).toISOString();
    expect(formatClockTime(today, "en-GB", now)).toBe("13:41");
    expect(formatClockTime(yesterday, "en-GB", now)).toContain("13:41");
    expect(formatClockTime(yesterday, "en-GB", now)).toContain("23");
  });
});

describe("bucketCoveragePct", () => {
  const cell = {
    periodStart: "2026-09-24T00:00:00Z",
    periodEnd: "2026-09-25T00:00:00Z",
    totalChecks: 960,
  };
  const now = new Date("2026-09-26T00:00:00Z").getTime();

  it("compares received probes to max(1, regions) / period over the bucket", () => {
    // 1-minute period, one region: 1440 expected, 960 received → 67%.
    expect(bucketCoveragePct(cell, probesPerSecond(60_000, 1), { now })).toBe(67);
    // Same count over two regions → a third.
    expect(bucketCoveragePct(cell, probesPerSecond(60_000, 2), { now })).toBe(33);
  });

  it("clamps to the check's creation and to now, and caps at 100", () => {
    expect(
      bucketCoveragePct(cell, probesPerSecond(60_000, 1), {
        now,
        measuredFrom: "2026-09-24T08:00:00Z",
      }),
    ).toBe(100);
    expect(
      bucketCoveragePct({ ...cell, totalChecks: 10 }, probesPerSecond(60_000, 1), {
        now: new Date("2026-09-24T00:10:00Z").getTime(),
      }),
    ).toBe(100);
  });

  it("has nothing to say without a period", () => {
    expect(bucketCoveragePct(cell, undefined, { now })).toBeNull();
    expect(probesPerSecond(undefined, 3)).toBeUndefined();
  });
});
