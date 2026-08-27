/**
 * @vitest-environment jsdom
 *
 * Spec 2026-08-22-07 §2: the two passes must be two RENDERS, not one blocked
 * one, and pass 2 must not unmount or remount the chart — an active zoom and a
 * pinned selection are controlled props, so a remount is the one thing that
 * would silently drop them.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";

// jsdom ships neither ResizeObserver (the chart measures its wrapper to place
// the pinned box) nor layout, so recharts' ResponsiveContainer renders at 0×0.
// Neither matters here: every assertion is about which BRANCH the component
// renders and whether that subtree survives pass 2.
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

const mocks = vi.hoisted(() => ({
  useChartWindowResults: vi.fn(),
  useRegions: vi.fn(() => ({ data: { regions: [] } })),
  // The availability strip's own fetch. This file is about the RESULTS passes,
  // so it is parked in a settled-empty state: the strip then renders nothing and
  // cannot change which branch the assertions below are reading.
  useCheckAvailabilityBuckets: vi.fn(() => ({
    data: undefined,
    isLoading: false,
  })),
}));

vi.mock("@/api/hooks", () => ({
  useChartWindowResults: mocks.useChartWindowResults,
  useRegions: mocks.useRegions,
  useCheckAvailabilityBuckets: mocks.useCheckAvailabilityBuckets,
}));

// The pinned detail box owns a router <Link> and its own query; stub it. What
// this file asserts about it is that the CHART keeps rendering it with the same
// controlled selection across pass 2 — not what the box itself draws.
vi.mock("@/components/checks/pinned-result-box", () => ({
  PinnedResultBox: ({ resultUid }: { resultUid: string }) => (
    <div data-testid="pinned-result-box">{resultUid}</div>
  ),
}));

const { ResponseTimeChart } = await import("./response-time-chart");

const ONE_MINUTE = 60_000;
const ONE_HOUR = 3_600_000;
const PINNED_UID = "pinned-rollup-row";

function rollupRow(uid: string, msAgo: number) {
  return {
    uid,
    region: "eu",
    periodType: "hour",
    periodStart: new Date(Date.now() - msAgo).toISOString(),
    durationMs: 100,
    status: "up",
  };
}

function rawRow(uid: string, msAgo: number) {
  return {
    uid,
    region: "eu",
    periodType: "raw",
    periodStart: new Date(Date.now() - msAgo).toISOString(),
    durationMs: 120,
    status: "up",
  };
}

const ROLLUPS = Array.from({ length: 12 }, (_, i) =>
  rollupRow(i === 3 ? PINNED_UID : `h-${i}`, (i + 2) * ONE_HOUR),
);

function state(rows: unknown[], extra: Record<string, unknown> = {}) {
  return {
    data: { data: rows },
    isLoading: false,
    isFetching: false,
    error: null,
    rawError: null,
    rawPending: false,
    // Defaults to "settled": most cases in this file are asserting about a
    // pass that has already resolved. Cases about the loading/empty flash
    // (spec 2026-08-25-03) override this explicitly.
    isEmptyPending: false,
    ...extra,
  };
}

/** A zoomed, pinned chart — the two pieces of state pass 2 must not disturb. */
const to = Date.now();
const from = to - 30 * 24 * ONE_HOUR;

function renderChart() {
  return render(
    <ResponseTimeChart
      org="acme"
      checkUid="check-1"
      periodMs={ONE_MINUTE}
      initialPeriod="month"
      zoomFrom={from}
      zoomTo={to}
      selectedUid={PINNED_UID}
    />,
  );
}

describe("progressive chart render", () => {
  beforeEach(() => {
    mocks.useChartWindowResults.mockReset();
    mocks.useRegions.mockReturnValue({ data: { regions: [] } });
  });

  afterEach(cleanup);

  it("shows the skeleton only while pass 1 is loading", () => {
    mocks.useChartWindowResults.mockReturnValue(
      state([], { isLoading: true, rawPending: true }),
    );
    renderChart();

    expect(screen.queryByTestId("response-time-chart-wrapper")).toBeNull();
  });

  it("draws pass-1 data while pass 2 is still pending, and keeps it across the merge", () => {
    mocks.useChartWindowResults.mockReturnValue(
      state(ROLLUPS, { rawPending: true }),
    );
    const view = renderChart();

    // Drawn from rollups alone — not a skeleton, not "no data".
    const wrapper = screen.getByTestId("response-time-chart-wrapper");
    expect(wrapper).toBeTruthy();
    expect(view.container.querySelector(".animate-pulse")).toBeNull();
    expect(screen.queryByTestId("response-time-chart-no-data")).toBeNull();
    // The zoom control and the pinned box are both live before raw arrives.
    expect(screen.getByText("Reset zoom")).toBeTruthy();
    expect(screen.getByTestId("pinned-result-box")).toBeTruthy();

    // Pass 2 resolves and merges into the same series.
    mocks.useChartWindowResults.mockReturnValue(
      state([rawRow("r-1", ONE_MINUTE), ...ROLLUPS]),
    );
    view.rerender(
      <ResponseTimeChart
        org="acme"
        checkUid="check-1"
        periodMs={ONE_MINUTE}
        initialPeriod="month"
        zoomFrom={from}
        zoomTo={to}
        selectedUid={PINNED_UID}
      />,
    );

    // The very same DOM node: no unmount, no remount, so nothing that lives in
    // the chart's own state (zoom drag, hover) could have been reset.
    expect(screen.getByTestId("response-time-chart-wrapper")).toBe(wrapper);
    expect(view.container.querySelector(".animate-pulse")).toBeNull();
    expect(screen.queryByTestId("response-time-chart-no-data")).toBeNull();
    expect(screen.getByText("Reset zoom")).toBeTruthy();
    expect(screen.getByTestId("pinned-result-box")).toBeTruthy();
  });

  it("keeps pass-1 data on screen and says so when pass 2 fails", () => {
    mocks.useChartWindowResults.mockReturnValue(
      state(ROLLUPS, { rawError: new Error("raw boom") }),
    );
    renderChart();

    expect(screen.getByTestId("response-time-chart-wrapper")).toBeTruthy();
    const note = screen.getByTestId("response-time-chart-raw-error");
    expect(note.textContent).toContain("Latest points unavailable");
    expect(note.textContent).not.toContain("checks:detail");
  });

  it("says nothing about raw when pass 2 succeeded", () => {
    // Positive control for the notice above.
    mocks.useChartWindowResults.mockReturnValue(state(ROLLUPS));
    renderChart();

    expect(screen.queryByTestId("response-time-chart-raw-error")).toBeNull();
  });

  // Spec 2026-08-25-03: the check page's chart rendered a terminal, negative
  // "No data available" during a phase that is really just "the answer isn't
  // in yet" — pass 1 settled with zero rows, pass 2 (now enabled) still
  // pending. `isEmptyPending` is the hook's honest answer to "is the window
  // known to be empty, or do we simply not know yet".
  //
  // Deliberately NOT using renderChart()/its zoom props: a zoom (or
  // full-range) pins the x-domain and boundary-clamps the series even with
  // zero rows (response-time-chart.tsx:489, "still show the window and the
  // Reset control"), so chartData is never actually empty in that mode —
  // it would make chartData.length === 0 unreachable and the assertions
  // below vacuous.
  function renderUnzoomedChart(period: "day" | "week" = "day") {
    return render(
      <ResponseTimeChart
        org="acme"
        checkUid="check-1"
        periodMs={ONE_MINUTE}
        initialPeriod={period}
      />,
    );
  }

  it("shows the skeleton, not \"no data\", when pass 1 settled empty and pass 2 is still pending", () => {
    mocks.useChartWindowResults.mockReturnValue(
      state([], { rawPending: true, isEmptyPending: true }),
    );
    const view = renderUnzoomedChart();

    expect(view.container.querySelector(".animate-pulse")).toBeTruthy();
    expect(screen.queryByTestId("response-time-chart-no-data")).toBeNull();
    expect(screen.queryByTestId("response-time-chart-wrapper")).toBeNull();
  });

  it("shows \"no data\" once the whole window has settled with nothing", () => {
    // Positive control for the case above — without it, deleting the empty
    // state entirely would make the previous test pass too.
    mocks.useChartWindowResults.mockReturnValue(
      state([], { isEmptyPending: false }),
    );
    renderUnzoomedChart();

    expect(screen.getByTestId("response-time-chart-no-data")).toBeTruthy();
    expect(screen.queryByTestId("response-time-chart-wrapper")).toBeNull();
  });

  it("shows the skeleton, not \"no data\", right after a range switch lands on a fresh pending-empty window", () => {
    // Some data on screen (unzoomed, so this is real data, not a boundary
    // clamp), raw still merging.
    mocks.useChartWindowResults.mockReturnValue(
      state(ROLLUPS, { rawPending: true }),
    );
    const view = renderUnzoomedChart("day");
    expect(screen.getByTestId("response-time-chart-wrapper")).toBeTruthy();

    // updateTimeRange (response-time-chart.tsx:415) re-keys BOTH query keys on
    // every range switch, so nothing is cached under the new keys and the
    // hook re-enters the sequence from scratch — including the empty-pass-1
    // step this spec fixes.
    mocks.useChartWindowResults.mockReturnValue(
      state([], { rawPending: true, isEmptyPending: true }),
    );
    view.rerender(
      <ResponseTimeChart
        org="acme"
        checkUid="check-1"
        periodMs={ONE_MINUTE}
        initialPeriod="week"
      />,
    );

    expect(view.container.querySelector(".animate-pulse")).toBeTruthy();
    expect(screen.queryByTestId("response-time-chart-no-data")).toBeNull();
  });
});
