/**
 * @vitest-environment jsdom
 *
 * Spec 2026-08-28-03: the response-time chart decides "failing = red" in
 * three places — the line gradient, the always-render-dot predicate, and
 * each per-point circle's fill — and only the first two agreed. The
 * activeDot (hover) renderer hardcoded `status === "down" || "unknown"`,
 * so an `error` or `timeout` point rendered a GREEN dot sitting on a RED
 * line. dotFillColor() is the exact function every fill decision in
 * response-time-chart.tsx now calls (single- and multi-series, regular
 * "dot" and "activeDot" alike) — testing it directly exercises the real
 * call the renderers make, not a re-implementation of it.
 */
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { dotFillColor } from "./response-time-chart";

const DOWN = "hsl(0, 72%, 51%)";
const UP = "hsl(142, 76%, 36%)";
const REGION_COLOR = "var(--chart-2)"; // stand-in for a multi-series line color

describe("chart dot fill color", () => {
  it("renders down, error and timeout as the same red as the line gradient", () => {
    // This is the exact bug: the old activeDot code only matched "down",
    // so "error" and "timeout" fell through to the neutral (up) color even
    // though the line itself goes red for them (isFailingStatus, ../
    // response-time-chart.tsx). Against the pre-fix hardcoded check
    // (`status === "down" || status === "unknown"`) these two assertions
    // would have failed.
    expect(dotFillColor("down", DOWN, UP)).toBe(DOWN);
    expect(dotFillColor("error", DOWN, UP)).toBe(DOWN);
    expect(dotFillColor("timeout", DOWN, UP)).toBe(DOWN);
    expect(dotFillColor("unknown", DOWN, UP)).toBe(DOWN);
  });

  it("keeps warning, degraded and abandoned non-red", () => {
    expect(dotFillColor("warning", DOWN, UP)).toBe(UP);
    expect(dotFillColor("degraded", DOWN, UP)).toBe(UP);
    expect(dotFillColor("abandoned", DOWN, UP)).toBe(UP);
  });

  it("leaves genuinely up/in-progress statuses neutral", () => {
    expect(dotFillColor("up", DOWN, UP)).toBe(UP);
    expect(dotFillColor("ok", DOWN, UP)).toBe(UP);
    expect(dotFillColor("created", DOWN, UP)).toBe(UP);
    expect(dotFillColor("running", DOWN, UP)).toBe(UP);
  });

  it("still picks the down color over a region's own line color in multi-series mode", () => {
    // Multi-series dots fall back to the region's identity color (not a flat
    // COLOR_UP) when not failing — but a failing point must still go red,
    // never the region color.
    expect(dotFillColor("error", DOWN, REGION_COLOR)).toBe(DOWN);
    expect(dotFillColor("timeout", DOWN, REGION_COLOR)).toBe(DOWN);
    expect(dotFillColor("up", DOWN, REGION_COLOR)).toBe(REGION_COLOR);
  });

  // Guards against the fill decision drifting back into a hardcoded,
  // per-callsite comparison (the original bug) instead of calling
  // dotFillColor()/isFailingStatus(). A future edit that reintroduces
  // `payload.status === "down"` here would fail this test even though
  // dotFillColor() itself stays correct.
  it("is the only place response-time-chart.tsx decides a dot's fill from payload.status", () => {
    const source = readFileSync(
      join(__dirname, "response-time-chart.tsx"),
      "utf8",
    );
    const fillDecisions = [...source.matchAll(/const fill = ([^;]+);/g)].map(
      (m) => m[1].trim(),
    );
    expect(fillDecisions.length).toBeGreaterThanOrEqual(4);
    for (const decision of fillDecisions) {
      expect(decision).toMatch(/^dotFillColor\(payload\.status, /);
    }
  });
});
