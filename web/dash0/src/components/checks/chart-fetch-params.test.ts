import { describe, expect, it } from "vitest";

import {
  chartFetchParams,
  type ChartTierFetch,
} from "@/components/checks/response-time-chart";

const RANGES = ["hour", "day", "week", "month"] as const;
/** Around the 5-minute raw/hour threshold, plus the undefined case. */
const PERIODS_MS = [
  undefined,
  10_000,
  60_000,
  4 * 60_000,
  5 * 60_000,
  15 * 60_000,
  60 * 60_000,
];
const ONE_HOUR = 3_600_000;
const ONE_DAY = 24 * ONE_HOUR;
/** Spans straddling every rangeForSpan() boundary. */
const ZOOM_SPANS_MS = [
  60_000,
  ONE_HOUR,
  ONE_HOUR + 1,
  ONE_DAY,
  ONE_DAY + 1,
  7 * ONE_DAY,
  7 * ONE_DAY + 1,
  30 * ONE_DAY,
];

const ROLLUP_TIERS = ["hour", "day", "month"];

function tiersOf(entry: ChartTierFetch): string[] {
  return entry.periodType.split(",");
}

/** Every (range × periodMs × zoom?) combination the chart can ask for. */
function allPlans(): { label: string; plan: ChartTierFetch[] }[] {
  const plans: { label: string; plan: ChartTierFetch[] }[] = [];

  for (const range of RANGES) {
    for (const periodMs of PERIODS_MS) {
      plans.push({
        label: `range=${range} periodMs=${periodMs}`,
        plan: chartFetchParams(range, periodMs),
      });

      for (const span of ZOOM_SPANS_MS) {
        const to = Date.UTC(2026, 7, 22, 12, 0, 0);
        plans.push({
          label: `range=${range} periodMs=${periodMs} zoomSpan=${span}`,
          plan: chartFetchParams(range, periodMs, { from: to - span, to }),
        });
      }
    }
  }

  return plans;
}

describe("chartFetchParams tier plan", () => {
  // THE guard for spec 2026-08-22-04: `results` carries exactly two useful
  // indexes and both are partial, split on `period_type = 'raw'`. A single
  // query naming raw AND a rollup tier is implied by neither, so Postgres can
  // only answer it by sequentially scanning the largest table in the system.
  // This is the cheap check that keeps the fix from eroding.
  it("never mixes raw with a rollup tier in one query", () => {
    for (const { label, plan } of allPlans()) {
      for (const entry of plan) {
        const tiers = tiersOf(entry);
        const hasRaw = tiers.includes("raw");
        const hasRollup = tiers.some((t) => ROLLUP_TIERS.includes(t));
        expect(
          hasRaw && hasRollup,
          `${label}: periodType "${entry.periodType}" straddles the raw/rollup index split`,
        ).toBe(false);
        expect(tiers.length, `${label}: empty tier entry`).toBeGreaterThan(0);
        for (const tier of tiers) {
          expect(
            ["raw", ...ROLLUP_TIERS],
            `${label}: unknown tier "${tier}"`,
          ).toContain(tier);
        }
      }
    }
  });

  it("emits at most two entries — one rollup, exactly one raw", () => {
    for (const { label, plan } of allPlans()) {
      expect(plan.length, label).toBeGreaterThanOrEqual(1);
      expect(plan.length, label).toBeLessThanOrEqual(2);

      const rawEntries = plan.filter((e) => tiersOf(e).includes("raw"));
      expect(
        rawEntries.length,
        `${label}: raw must be asked for exactly once`,
      ).toBe(1);
      expect(
        plan.length - rawEntries.length,
        `${label}: at most one rollup query`,
      ).toBeLessThanOrEqual(1);
    }
  });

  // The split must not lose data: the union of the tiers asked for has to be
  // exactly the set the old single mixed query named.
  it("covers the same tier set the pre-split mixed query used", () => {
    const expected = (
      range: string,
      periodMs: number | undefined,
    ): string[] => {
      const dense = (periodMs ?? 60_000) < 5 * 60_000;
      if (range === "month") return ["raw", "hour", "day"];
      if (range === "week") return ["raw", "hour"];
      if (range === "day") return dense ? ["raw", "hour"] : ["raw"];
      return ["raw"];
    };
    const rangeForSpan = (spanMs: number): string =>
      spanMs <= ONE_HOUR
        ? "hour"
        : spanMs <= ONE_DAY
          ? "day"
          : spanMs <= 7 * ONE_DAY
            ? "week"
            : "month";

    for (const range of RANGES) {
      for (const periodMs of PERIODS_MS) {
        const plan = chartFetchParams(range, periodMs);
        expect(
          plan.flatMap(tiersOf).sort(),
          `range=${range} periodMs=${periodMs}`,
        ).toEqual(expected(range, periodMs).sort());

        for (const span of ZOOM_SPANS_MS) {
          const to = Date.UTC(2026, 7, 22, 12, 0, 0);
          const zoomed = chartFetchParams(range, periodMs, {
            from: to - span,
            to,
          });
          expect(
            zoomed.flatMap(tiersOf).sort(),
            `range=${range} periodMs=${periodMs} zoomSpan=${span}`,
          ).toEqual(expected(rangeForSpan(span), periodMs).sort());
        }
      }
    }
  });

  it("gives every tier the same window and the same projection", () => {
    for (const { label, plan } of allPlans()) {
      const [first] = plan;
      for (const entry of plan) {
        expect(entry.periodStartAfter, label).toBe(first.periodStartAfter);
        expect(entry.periodEndBefore, label).toBe(first.periodEndBefore);
        expect(entry.with, label).toBe(first.with);
        expect(entry.size, label).toBe(1000);
        // No blob field is ever requested — that is what lets the server drop
        // metrics/output from the projection (spec 2026-08-22-04 §3).
        expect(entry.with, label).not.toContain("metrics");
        expect(entry.with, label).not.toContain("output");
      }
    }
  });

  it("bounds a zoomed window at both ends and an unzoomed one only at the start", () => {
    const to = Date.UTC(2026, 7, 22, 12, 0, 0);
    const from = to - 2 * ONE_HOUR;

    for (const entry of chartFetchParams("month", 60_000, { from, to })) {
      expect(entry.periodStartAfter).toBe(new Date(from).toISOString());
      expect(entry.periodEndBefore).toBe(new Date(to).toISOString());
    }

    for (const entry of chartFetchParams("month", 60_000)) {
      expect(entry.periodEndBefore).toBeUndefined();
    }
  });
});
