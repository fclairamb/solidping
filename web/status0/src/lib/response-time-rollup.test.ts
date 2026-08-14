import { describe, test, expect } from "bun:test";
import type { ResponseTimeSeries } from "@/api/hooks";
import { buildCombinedRows, severityRank, STATUS_SEVERITY } from "./response-time-rollup";

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
