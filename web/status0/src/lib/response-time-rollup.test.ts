import { describe, test, expect } from "bun:test";
import type { ResponseTimeSeries } from "@/api/hooks";
import {
  buildCombinedRows,
  expandTimeGaps,
  pointKey,
  samplingInterval,
  severityRank,
  STATUS_SEVERITY,
} from "./response-time-rollup";

// Direct unit coverage for the "worst status wins" incident-strip rollup
// (spec 2026-08-14-04, Proposal item 5). The E2E suite
// (e2e/response-time-chart.spec.ts) only ever used status: "up" for every
// point, so the rollup reducer never actually ran a mixed-status case there —
// these tests exercise buildCombinedRows directly with real severity
// differences so an inverted ranking or a wrong-winner reducer bug fails.

describe("severityRank", () => {
  test("pins the severity ordering: down > error > timeout > degraded/warning > up", () => {
    expect(severityRank("down")).toBeGreaterThan(severityRank("error"));
    expect(severityRank("error")).toBeGreaterThan(severityRank("timeout"));
    expect(severityRank("timeout")).toBeGreaterThan(severityRank("degraded"));
    expect(severityRank("timeout")).toBeGreaterThan(severityRank("warning"));
    expect(severityRank("degraded")).toBeGreaterThan(severityRank("up"));
    expect(severityRank("warning")).toBeGreaterThan(severityRank("up"));
  });

  test("statuses with no incident meaning rank the same as up (0)", () => {
    expect(severityRank("up")).toBe(0);
    expect(severityRank("created")).toBe(0);
    expect(severityRank("running")).toBe(0);
    expect(severityRank("unknown")).toBe(0);
    expect(severityRank(undefined)).toBe(0);
  });

  test("every entry in the exported severity table outranks up", () => {
    for (const status of Object.keys(STATUS_SEVERITY)) {
      expect(STATUS_SEVERITY[status]).toBeGreaterThan(0);
    }
  });
});

describe("buildCombinedRows — worst-status-wins rollup", () => {
  const T0 = "2026-08-14T10:00:00Z";
  const T1 = "2026-08-14T10:01:00Z";

  test("one region down, another up at the SAME timestamp: strip reads down, not up", () => {
    const series: ResponseTimeSeries[] = [
      { region: "eu2", points: [{ time: T0, durationP95: 40, status: "up" }] },
      {
        region: "us1",
        points: [{ time: T0, durationP95: 999, status: "down" }],
      },
    ];

    const rows = buildCombinedRows(series);
    expect(rows).toHaveLength(1);
    expect(rows[0].status).toBe("down");
  });

  test("worst-status-wins is order-independent — the down region can be first or second", () => {
    const downFirst: ResponseTimeSeries[] = [
      {
        region: "us1",
        points: [{ time: T0, durationP95: 999, status: "down" }],
      },
      { region: "eu2", points: [{ time: T0, durationP95: 40, status: "up" }] },
    ];
    const upFirst: ResponseTimeSeries[] = [
      { region: "eu2", points: [{ time: T0, durationP95: 40, status: "up" }] },
      {
        region: "us1",
        points: [{ time: T0, durationP95: 999, status: "down" }],
      },
    ];

    expect(buildCombinedRows(downFirst)[0].status).toBe("down");
    expect(buildCombinedRows(upFirst)[0].status).toBe("down");
  });

  test("three-way ordering at a shared timestamp: down beats degraded beats up", () => {
    const series: ResponseTimeSeries[] = [
      { region: "a", points: [{ time: T0, durationP95: 10, status: "up" }] },
      {
        region: "b",
        points: [{ time: T0, durationP95: 20, status: "degraded" }],
      },
      { region: "c", points: [{ time: T0, durationP95: 30, status: "down" }] },
    ];

    expect(buildCombinedRows(series)[0].status).toBe("down");

    // Drop the down region: degraded must now beat up.
    const withoutDown = series.filter((s) => s.region !== "c");
    expect(buildCombinedRows(withoutDown)[0].status).toBe("degraded");
  });

  test("per-timestamp rollup: each row's worst status is independent of the others", () => {
    const series: ResponseTimeSeries[] = [
      {
        region: "eu2",
        points: [
          { time: T0, durationP95: 40, status: "up" },
          { time: T1, durationP95: 45, status: "up" },
        ],
      },
      {
        region: "us1",
        points: [
          { time: T0, durationP95: 999, status: "timeout" },
          { time: T1, durationP95: 160, status: "up" },
        ],
      },
    ];

    const rows = buildCombinedRows(series);
    expect(rows).toHaveLength(2);
    const byTime = new Map(rows.map((r) => [r.time, r]));
    expect(byTime.get(T0)?.status).toBe("timeout");
    expect(byTime.get(T1)?.status).toBe("up");
  });

  test("a region absent at this timestamp never contributes a phantom vote", () => {
    // Only "eu2" has a point at T0; "us1" only reports at T1. The rollup at
    // T0 must reflect eu2's real status, not an undefined/up vote manufactured
    // for the absent us1 slot.
    const series: ResponseTimeSeries[] = [
      {
        region: "eu2",
        points: [{ time: T0, durationP95: 40, status: "error" }],
      },
      {
        region: "us1",
        points: [{ time: T1, durationP95: 160, status: "up" }],
      },
    ];

    const rows = buildCombinedRows(series);
    const byTime = new Map(rows.map((r) => [r.time, r]));
    expect(byTime.get(T0)?.status).toBe("error");
    expect(byTime.get(T1)?.status).toBe("up");
  });
});

describe("buildCombinedRows — staggered regions land in shared slots", () => {
  // Real worker phases, taken from a live 5-region check: every region samples
  // once a minute but each on its own second-of-the-minute, spread over 45s.
  const PHASE_SECONDS = [58, 8, 26, 31, 43];
  const REGIONS = ["jp1", "us1", "aws-paris", "default", "eu2"];
  const SAMPLES = 10;

  function staggeredSeries(): ResponseTimeSeries[] {
    return REGIONS.map((region, r) => ({
      region,
      points: Array.from({ length: SAMPLES }, (_, i) => ({
        // jp1 fires at :25:58 — i.e. BEFORE the minute the others report in —
        // so this also pins that a slot is not just "same minute".
        time: new Date(
          Date.UTC(2026, 7, 16, 18, 26 + i, PHASE_SECONDS[r] - (r === 0 ? 60 : 0)),
        ).toISOString(),
        durationP95: 10 + r,
        status: "up",
      })),
    }));
  }

  test("one row per sample, every region present — not one row per region-sample", () => {
    const rows = buildCombinedRows(staggeredSeries());

    // The bug this pins: keyed on the raw timestamp these 50 points pivot into
    // 50 rows holding ONE value each, and connectNulls={false} then draws
    // nothing at all.
    expect(rows).toHaveLength(SAMPLES);
    for (const row of rows) {
      for (let r = 0; r < REGIONS.length; r++) {
        expect(row[pointKey(r)]).toBe(10 + r);
      }
    }
  });

  test("every region has an unbroken run of values, so every line renders", () => {
    const rows = buildCombinedRows(staggeredSeries());

    for (let r = 0; r < REGIONS.length; r++) {
      let longestRun = 0;
      let run = 0;
      for (const row of rows) {
        run = row[pointKey(r)] != null ? run + 1 : 0;
        longestRun = Math.max(longestRun, run);
      }
      // A run of 1 draws no line segment — that is exactly the blank chart.
      expect(longestRun).toBe(SAMPLES);
    }
  });

  test("slots stay time-ordered and never merge two samples of one region", () => {
    const rows = buildCombinedRows(staggeredSeries());

    const times = rows.map((r) => Date.parse(r.time));
    expect([...times].sort((a, b) => a - b)).toEqual(times);

    // Every region contributed all of its samples: none was overwritten by a
    // later sample of the same region landing in the same slot.
    const total = rows.reduce(
      (sum, row) =>
        sum +
        REGIONS.filter((_, r) => row[pointKey(r)] != null).length,
      0,
    );
    expect(total).toBe(REGIONS.length * SAMPLES);
  });

  test("a region sampling far apart is NOT dragged into a neighbour's slot", () => {
    // fast: every minute. slow: every 5 minutes, on the same wall clock.
    const fast: ResponseTimeSeries = {
      region: "fast",
      points: Array.from({ length: 10 }, (_, i) => ({
        time: new Date(Date.UTC(2026, 7, 16, 18, i, 2)).toISOString(),
        durationP95: 10,
        status: "up",
      })),
    };
    const slow: ResponseTimeSeries = {
      region: "slow",
      points: [0, 5].map((m) => ({
        time: new Date(Date.UTC(2026, 7, 16, 18, m, 9)).toISOString(),
        durationP95: 90,
        status: "up",
      })),
    };

    const rows = buildCombinedRows([fast, slow]);

    // The finest interval (60s) sets the slot size, so the slow region keeps
    // exactly two points, aligned with the fast region's 18:00 and 18:05.
    expect(rows).toHaveLength(10);
    expect(rows.filter((r) => r[pointKey(1)] != null)).toHaveLength(2);
    expect(rows[0][pointKey(1)]).toBe(90);
    expect(rows[5][pointKey(1)]).toBe(90);
  });

  test("worst-status rollup still applies inside a slot, not just on exact ties", () => {
    const series: ResponseTimeSeries[] = [
      {
        region: "eu2",
        points: [{ time: "2026-08-16T18:26:03Z", durationP95: 40, status: "up" }],
      },
      {
        region: "us1",
        points: [
          // 9s later — a different timestamp, the same slot.
          { time: "2026-08-16T18:26:12Z", durationP95: 999, status: "down" },
        ],
      },
      {
        region: "jp1",
        points: [
          { time: "2026-08-16T18:25:03Z", durationP95: 40, status: "up" },
          { time: "2026-08-16T18:26:07Z", durationP95: 40, status: "up" },
        ],
      },
    ];

    const rows = buildCombinedRows(series);
    const slot = rows.find((r) => r[pointKey(0)] != null);
    expect(slot?.status).toBe("down");
  });
});

describe("samplingInterval", () => {
  test("returns the finest median interval across series", () => {
    const series: ResponseTimeSeries[] = [
      {
        region: "slow",
        points: [0, 300, 600].map((s) => ({
          time: new Date(Date.UTC(2026, 7, 16, 18, 0, s)).toISOString(),
          durationP95: 1,
          status: "up",
        })),
      },
      {
        region: "fast",
        points: [0, 60, 120].map((s) => ({
          time: new Date(Date.UTC(2026, 7, 16, 18, 0, s)).toISOString(),
          durationP95: 1,
          status: "up",
        })),
      },
    ];

    expect(samplingInterval(series)).toBe(60_000);
  });

  test("one isolated gap does not stretch the interval (median, not mean)", () => {
    const series: ResponseTimeSeries[] = [
      {
        region: "eu2",
        points: [0, 60, 120, 3600].map((s) => ({
          time: new Date(Date.UTC(2026, 7, 16, 18, 0, s)).toISOString(),
          durationP95: 1,
          status: "up",
        })),
      },
    ];

    expect(samplingInterval(series)).toBe(60_000);
  });

  test("null when no series has two points — callers fall back to exact grouping", () => {
    expect(samplingInterval([])).toBeNull();
    expect(
      samplingInterval([
        {
          region: "eu2",
          points: [{ time: "2026-08-16T18:00:00Z", durationP95: 1, status: "up" }],
        },
      ]),
    ).toBeNull();
  });
});

// Spec 2026-08-26-10 phase 2: a slot's availability is the SUM of the regions
// that reported in it, never an average of their percentages.
describe("buildCombinedRows availability summing", () => {
  const point = (
    time: string,
    totalChecks?: number,
    successfulChecks?: number,
  ) => ({
    time,
    durationP95: 100,
    status: "up",
    totalChecks,
    successfulChecks,
    availabilityPct:
      totalChecks && totalChecks > 0
        ? ((successfulChecks ?? 0) / totalChecks) * 100
        : undefined,
  });

  // Two points per region so a sampling interval is derivable (one point each
  // would leave tolerance at 0 and put every timestamp in its own slot), with
  // the regions phase-shifted by five seconds the way real workers are.
  const twoRegions = (
    euFirst: ReturnType<typeof point>,
    usFirst: ReturnType<typeof point>,
  ): ResponseTimeSeries[] => [
    {
      region: "eu",
      points: [euFirst, point("2026-08-26T10:01:00Z", 60, 60)],
    },
    {
      region: "us",
      points: [usFirst, point("2026-08-26T10:01:05Z", 60, 60)],
    },
  ];

  test("sums up/total across the regions in one slot", () => {
    const rows = buildCombinedRows(
      twoRegions(
        point("2026-08-26T10:00:00Z", 60, 60),
        point("2026-08-26T10:00:05Z", 1, 0),
      ),
    );

    expect(rows).toHaveLength(2);
    expect(rows[0].availTotal).toBe(61);
    expect(rows[0].availUp).toBe(60);
    // The positive control against averaging: averaging the two regions'
    // percentages would have produced 50%, a different number entirely.
    expect(
      ((rows[0].availUp ?? 0) / (rows[0].availTotal ?? 1)) * 100,
    ).toBeCloseTo(98.36, 1);
  });

  test("a point with no countable probe contributes nothing, not zero", () => {
    const rows = buildCombinedRows(
      twoRegions(
        point("2026-08-26T10:00:00Z", 60, 60),
        // A lifecycle marker: the server sends no counts at all for it.
        point("2026-08-26T10:00:05Z"),
      ),
    );

    expect(rows[0].availTotal).toBe(60);
    expect(rows[0].availUp).toBe(60);
  });

  test("leaves the counters undefined when no region reported any probe", () => {
    const rows = buildCombinedRows(
      twoRegions(
        point("2026-08-26T10:00:00Z"),
        point("2026-08-26T10:00:05Z"),
      ),
    );

    expect(rows[0].availTotal).toBeUndefined();
    expect(rows[0].availUp).toBeUndefined();
  });
});

// Spec 2026-09-21-03, resolved open question: gap semantics on the numeric
// time axis are an acceptance criterion. connectNulls={false} only breaks a
// line at NULL rows, and a stretch where NO series reported contributes none
// itself — so an explicit null row must be inserted into every real gap, or
// the axis draws a straight line straight across an outage.
describe("expandTimeGaps", () => {
  const MIN = 60_000;
  const base = Date.UTC(2026, 8, 21, 12, 0, 0);
  const at = (ms: number) => ({ time: new Date(ms).toISOString() });

  test("a stretch of silence becomes a null row — the line breaks there", () => {
    // Samples at 1-minute cadence, then a 10-minute restart gap, then more
    // samples. The median interval stays 1 minute, so 10 minutes is a gap.
    const rows = [
      { ...at(base), durationP95: 40 },
      { ...at(base + MIN), durationP95: 41 },
      { ...at(base + 12 * MIN), durationP95: 42 },
      { ...at(base + 13 * MIN), durationP95: 43 },
    ];

    const expanded = expandTimeGaps(rows, (time) => ({ ...at(Date.parse(time)), durationP95: null }));

    expect(expanded).toHaveLength(5);
    // No value may straddle the gap: the inserted row is null, so the two
    // surviving runs are separated and connectNulls={false} draws no line
    // across the silence.
    const mid = expanded[2];
    expect(mid.durationP95).toBeNull();
    expect(mid.time).toBe(new Date(base + 6.5 * MIN).toISOString());
  });

  test("ordinary phase jitter never reads as a gap", () => {
    // Real worker phase spread: rows land seconds apart inside a 1-minute
    // cadence. Inserting gap rows there would shred the series into dots.
    const rows = [0, 1, 2, 3].map((i) => ({
      ...at(base + i * MIN + 7_000),
      durationP95: 40,
    }));

    expect(expandTimeGaps(rows, (time) => ({ ...at(Date.parse(time)), durationP95: null }))).toHaveLength(4);
  });

  test("a truly isolated sample stays isolated — it gets gaps on BOTH sides, not a bridge", () => {
    // A lone sample in the middle of a long silence (worker paused, then a
    // single probe, then silence again): the null rows on both sides keep
    // the line from reaching it, so the isolated-dot renderer is what puts
    // it on screen — and it must keep firing (it looks at the row array's
    // null neighbours, which this row now guarantees). The surrounding rows
    // are at the series' 1-minute cadence, so the median interval — and the
    // gap threshold — stay at 1 minute.
    const rows = [
      { ...at(base), durationP95: 40 },
      { ...at(base + MIN), durationP95: 40 },
      { ...at(base + 2 * MIN), durationP95: 40 },
      { ...at(base + 3 * MIN), durationP95: 40 },
      { ...at(base + 30 * MIN), durationP95: 41 },
      { ...at(base + 90 * MIN), durationP95: 42 },
      { ...at(base + 91 * MIN), durationP95: 42 },
    ];

    const expanded = expandTimeGaps(rows, (time) => ({ ...at(Date.parse(time)), durationP95: null }));

    expect(expanded).toHaveLength(9);
    // The isolated sample's neighbours are BOTH null now.
    expect(expanded[4].durationP95).toBeNull();
    expect(expanded[6].durationP95).toBeNull();
    expect(expanded[5].durationP95).toBe(41);
  });

  test("two regions covering disjoint time ranges (the 39-day fixture)", () => {
    // Region A: day rollups ending 13 Aug. Region B: raw points from the
    // last minutes. Merged rows jump 39 days mid-array; the expansion must
    // break BOTH series across that jump instead of letting recharts draw
    // one straight line from Aug 13 to Sep 21.
    const DAY = 24 * 60 * 60_000;
    const aRows = [0, 1, 2].map((i) => ({
      ...at(base - 45 * DAY + i * DAY),
      durationP95: 40,
    }));
    const bRows = [0, 1, 2].map((i) => ({
      ...at(base - 2 * MIN + i * MIN),
      durationP95: 90,
    }));

    const expanded = expandTimeGaps([...aRows, ...bRows], (time) => ({
      ...at(Date.parse(time)),
      durationP95: null,
    }));

    // 6 real rows + 1 gap row for the 39-day silence.
    expect(expanded).toHaveLength(7);
    const gap = expanded[3];
    expect(gap.durationP95).toBeNull();
    expect(Date.parse(gap.time)).toBeGreaterThan(Date.parse(expanded[2].time));
    expect(Date.parse(gap.time)).toBeLessThan(Date.parse(expanded[4].time));
  });

  test("a one-row series is returned unchanged", () => {
    const rows = [{ ...at(base), durationP95: 40 }];
    expect(expandTimeGaps(rows, (time) => ({ ...at(Date.parse(time)) }))).toHaveLength(1);
  });
});
