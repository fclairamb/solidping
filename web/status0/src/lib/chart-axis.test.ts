import { describe, test, expect } from "bun:test";
import { computeTimeAxis, formatAxisTick } from "./chart-axis";

// Spec 2026-09-21-03 B: the response-time chart renders a REAL time axis.
// The old category axis sampled five evenly spaced ROWS, so two adjacent
// ticks could be 39 days apart while the rest of the axis covered 16
// minutes. These tests pin the domain/tick math over the time domain.

const MIN = 5;
const HOUR = 3_600_000;
const DAY = 24 * HOUR;

describe("computeTimeAxis", () => {
  test("domain spans the real interval for disjoint series (the 39-day fixture)", () => {
    // One cluster of day rollups ending mid-August, one cluster of raw
    // points in the last minutes — exactly the disjoint-in-time chart the
    // spec fixes. The domain must cover Jul 21 → Sep 21, not stop at
    // either cluster.
    const start = Date.UTC(2026, 6, 21, 0, 0, 0);
    const end = Date.UTC(2026, 8, 21, 11, 49, 0);
    const times = [
      start,
      start + DAY,
      start + 39 * DAY,
      end - 5 * MIN,
      end,
    ];

    const axis = computeTimeAxis(times);
    expect(axis.domain[0]).toBe(start);
    expect(axis.domain[1]).toBe(end);
    expect(axis.domain[1] - axis.domain[0]).toBeGreaterThan(60 * DAY);
  });

  test("ticks are evenly spaced INSTANTS, not sampled rows", () => {
    // Two points 39 days apart plus a burst of points 20 seconds apart: on
    // a category axis the burst would own most of the axis. The ticks must
    // divide the full span evenly instead.
    const start = Date.UTC(2026, 6, 21, 0, 0, 0);
    const end = start + 40 * DAY;
    const burst = [0, 10, 20].map((s) => end - 20_000 + s * 1000);

    const axis = computeTimeAxis([start, ...burst]);
    expect(axis.ticks).toHaveLength(5);
    for (let i = 1; i < axis.ticks.length; i++) {
      expect(axis.ticks[i] - axis.ticks[i - 1]).toBeCloseTo(
        (end - start) / 4,
        -3,
      );
    }
  });

  test("single instant collapses to a one-tick axis, not a division by zero", () => {
    const t = Date.UTC(2026, 8, 21, 12, 0, 0);
    const axis = computeTimeAxis([t, t]);
    expect(axis.ticks).toEqual([t]);
    expect(axis.domain).toEqual([t, t]);
  });

  test("unparseable times are ignored", () => {
    const t0 = Date.UTC(2026, 8, 21, 12, 0, 0);
    const axis = computeTimeAxis([NaN, t0 - HOUR, t0, Number.NaN]);
    expect(axis.domain[0]).toBe(t0 - HOUR);
    expect(axis.domain[1]).toBe(t0);
  });

  test("no finite times yield an empty axis", () => {
    expect(computeTimeAxis([Number.NaN, Number.POSITIVE_INFINITY])).toEqual({
      domain: [0, 0],
      ticks: [],
    });
    expect(computeTimeAxis([])).toEqual({ domain: [0, 0], ticks: [] });
  });
});

describe("formatAxisTick", () => {
  const base = Date.UTC(2026, 8, 21, 12, 0, 0);

  test("sub-hour spans show the clock down to seconds", () => {
    const label = formatAxisTick(base + 30_000, 10 * MIN, "en-GB");
    expect(label).toMatch(/12:00:30/);
    // A sub-hour axis must NOT print a date — the old axis mixed them.
    expect(label).not.toMatch(/\d{4}|Sep|Aug/);
  });

  test("sub-day spans show hour:minute only", () => {
    const label = formatAxisTick(base + 5 * HOUR, 20 * HOUR, "en-GB");
    expect(label).toMatch(/17:00/);
    expect(label).not.toMatch(/Sep|Aug|2026/);
  });

  test("a few days show weekday + clock, so two ticks 12h apart never read the same", () => {
    const a = formatAxisTick(base - 36 * HOUR, 3 * DAY, "en-GB");
    const b = formatAxisTick(base - 12 * HOUR, 3 * DAY, "en-GB");
    expect(a).not.toBe(b);
    expect(a).toMatch(/Sat|Sun|Mon/);
    expect(a).toMatch(/0:00/);
  });

  test("multi-day spans print the date — no clock, no repeated day labels", () => {
    const a = formatAxisTick(Date.UTC(2026, 6, 21, 12, 0), 82 * DAY, "en-GB");
    const b = formatAxisTick(Date.UTC(2026, 7, 13, 2, 0), 82 * DAY, "en-GB");
    const c = formatAxisTick(Date.UTC(2026, 8, 21, 11, 49), 82 * DAY, "en-GB");
    expect(a).toMatch(/21 Jul|Jul 21/);
    expect(b).toMatch(/13 Aug|Aug 13/);
    expect(c).toMatch(/21 Sep|Sep 21/);
    // The old axis demoted repeated days to time-of-day, printing
    // "21 sept. · 11:44 · 11:49". Day-only labels cannot repeat a clock.
    expect(a).not.toMatch(/\d{1,2}:\d{2}/);
  });
});