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
}));

vi.mock("@/api/hooks", () => ({
  useChartWindowResults: mocks.useChartWindowResults,
  useRegions: mocks.useRegions,
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
});
