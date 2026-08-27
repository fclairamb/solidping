/**
 * @vitest-environment jsdom
 *
 * Spec 2026-08-26-10: the availability strip shares the chart's window, zoom and
 * region filter, re-buckets when any of them changes, renders gray cells for
 * no-data buckets, and disappears below the 3h floor in favour of a single
 * header figure.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  render as rtlRender,
  screen,
} from "@testing-library/react";
import type { ReactElement } from "react";
import { TooltipProvider } from "@/components/ui/tooltip";
import "@/i18n";

// The app mounts one TooltipProvider at the root (src/main.tsx); the strip's
// cells are Radix tooltip triggers, so the test has to reproduce it.
function render(ui: ReactElement) {
  return rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
}

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

const mocks = vi.hoisted(() => ({
  useChartWindowResults: vi.fn(),
  useRegions: vi.fn(() => ({ data: { regions: [] } })),
  useCheckAvailabilityBuckets: vi.fn(),
}));

vi.mock("@/api/hooks", () => ({
  useChartWindowResults: mocks.useChartWindowResults,
  useRegions: mocks.useRegions,
  useCheckAvailabilityBuckets: mocks.useCheckAvailabilityBuckets,
}));

vi.mock("@/components/checks/pinned-result-box", () => ({
  PinnedResultBox: () => null,
}));

const { ResponseTimeChart } = await import("./response-time-chart");

const HOUR_MS = 3_600_000;

function resultRow(uid: string, msAgo: number, region = "eu") {
  return {
    uid,
    region,
    periodType: "raw",
    periodStart: new Date(Date.now() - msAgo).toISOString(),
    durationMs: 120,
    status: "up",
  };
}

/** One strip cell, shaped like the API's AvailabilityBucket. */
function cell(
  startMsAgo: number,
  widthMs: number,
  pct: number | null,
  total = 60,
) {
  const start = new Date(Date.now() - startMsAgo);
  const hasData = pct !== null;
  return {
    periodStart: start.toISOString(),
    periodEnd: new Date(start.getTime() + widthMs).toISOString(),
    hasData,
    availabilityPct: pct,
    totalChecks: hasData ? total : 0,
    successfulChecks: hasData ? Math.round((pct / 100) * total) : 0,
    status: !hasData
      ? ("noData" as const)
      : pct >= 99.9
        ? ("up" as const)
        : pct >= 99
          ? ("degraded" as const)
          : ("down" as const),
  };
}

function bucketsResponse(cells: ReturnType<typeof cell>[], pct: number | null) {
  return {
    data: cells,
    window: {
      periodStart: new Date(Date.now() - 24 * HOUR_MS).toISOString(),
      periodEnd: new Date().toISOString(),
      hasData: pct !== null,
      availabilityPct: pct,
      totalChecks: pct === null ? 0 : 1440,
      successfulChecks: pct === null ? 0 : Math.round((pct / 100) * 1440),
      status: pct === null ? ("noData" as const) : ("up" as const),
    },
    bucketSeconds: 3600,
    windowStart: new Date(Date.now() - 24 * HOUR_MS).toISOString(),
    windowEnd: new Date().toISOString(),
  };
}

beforeEach(() => {
  mocks.useChartWindowResults.mockReturnValue({
    data: { data: [resultRow("r1", HOUR_MS), resultRow("r2", 2 * HOUR_MS)] },
    isLoading: false,
    rawError: null,
    isEmptyPending: false,
  });
  mocks.useCheckAvailabilityBuckets.mockReturnValue({
    data: bucketsResponse(
      [
        cell(3 * HOUR_MS, HOUR_MS, 100),
        cell(2 * HOUR_MS, HOUR_MS, 50),
        cell(HOUR_MS, HOUR_MS, null),
      ],
      99.95,
    ),
    isLoading: false,
  });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function lastQuery() {
  const calls = mocks.useCheckAvailabilityBuckets.mock.calls;
  return calls[calls.length - 1][2] as {
    from: string;
    to: string;
    bucketSeconds?: number;
    region?: string;
  };
}

describe("chart availability strip", () => {
  it("renders one cell per bucket, with a gray cell for the no-data bucket", () => {
    render(
      <ResponseTimeChart org="acme" checkUid="c1" initialPeriod="day" />,
    );

    const cells = screen.getAllByTestId(
      "response-time-chart-availability-strip-cell",
    );
    expect(cells).toHaveLength(3);
    expect(cells.map((c) => c.getAttribute("data-status"))).toEqual([
      "up",
      "down",
      "noData",
    ]);
  });

  it("labels each cell with its span and percentage, and no-data as such", () => {
    render(
      <ResponseTimeChart org="acme" checkUid="c1" initialPeriod="day" />,
    );

    const cells = screen.getAllByTestId(
      "response-time-chart-availability-strip-cell",
    );
    expect(cells[0].getAttribute("aria-label")).toContain("100%");
    expect(cells[1].getAttribute("aria-label")).toContain("50.0%");
    // The no-data cell must NOT claim a percentage — that is the whole point of
    // the third state.
    expect(cells[2].getAttribute("aria-label")).not.toContain("%");
    expect(cells[2].getAttribute("aria-label")).toContain("No data");
  });

  it("shows the window figure in the header for every range", () => {
    render(
      <ResponseTimeChart org="acme" checkUid="c1" initialPeriod="day" />,
    );

    expect(
      screen.getByTestId("response-time-chart-window-availability").textContent,
    ).toContain("99.95% availability");
  });

  it("renders no strip below the 3h floor, but keeps the header figure", () => {
    render(
      <ResponseTimeChart org="acme" checkUid="c1" initialPeriod="hour" />,
    );

    expect(
      screen.queryAllByTestId("response-time-chart-availability-strip-cell"),
    ).toHaveLength(0);
    expect(
      screen.getByTestId("response-time-chart-window-availability"),
    ).toBeTruthy();
    // With no strip the client leaves the width to the server rather than
    // asking for a one-cell strip.
    expect(lastQuery().bucketSeconds).toBeUndefined();
  });

  it("requests the spec's bucket width for each preset range", () => {
    render(
      <ResponseTimeChart org="acme" checkUid="c1" initialPeriod="day" />,
    );
    expect(lastQuery().bucketSeconds).toBe(3600);

    cleanup();
    render(
      <ResponseTimeChart org="acme" checkUid="c1" initialPeriod="week" />,
    );
    expect(lastQuery().bucketSeconds).toBe(6 * 3600);

    cleanup();
    render(
      <ResponseTimeChart org="acme" checkUid="c1" initialPeriod="month" />,
    );
    expect(lastQuery().bucketSeconds).toBe(24 * 3600);
  });

  it("re-buckets onto the zoom window when a drag-zoom is active", () => {
    const now = Date.now();
    const from = now - 12 * HOUR_MS;
    const to = now - 6 * HOUR_MS;

    render(
      <ResponseTimeChart
        org="acme"
        checkUid="c1"
        initialPeriod="month"
        zoomFrom={from}
        zoomTo={to}
      />,
    );

    const query = lastQuery();
    // The zoom window replaces the month range entirely — both edges AND the
    // bucket width, which must fall from the month's 1d to the zoom rule's 1h.
    expect(query.from).toBe(new Date(from).toISOString());
    expect(query.to).toBe(new Date(to).toISOString());
    expect(query.bucketSeconds).toBe(3600);
  });

  it("scopes the strip to the chart's region filter", () => {
    mocks.useRegions.mockReturnValue({
      data: { regions: [{ slug: "eu" }, { slug: "us" }] },
    });
    mocks.useChartWindowResults.mockReturnValue({
      data: {
        data: [
          resultRow("r1", HOUR_MS, "eu"),
          resultRow("r2", 2 * HOUR_MS, "us"),
        ],
      },
      isLoading: false,
      rawError: null,
      isEmptyPending: false,
    });

    render(
      <ResponseTimeChart
        org="acme"
        checkUid="c1"
        initialPeriod="day"
        region="us"
      />,
    );

    expect(lastQuery().region).toBe("us");
  });

  it("does not scope to a region the window never observed", () => {
    // The chart already guards against a stale ?region= slug; the strip must
    // follow it rather than silently querying a region that is not on screen.
    render(
      <ResponseTimeChart
        org="acme"
        checkUid="c1"
        initialPeriod="day"
        region="ap-southeast-9"
      />,
    );

    expect(lastQuery().region).toBeUndefined();
  });

  it("renders no percentage at all when the window has no data", () => {
    mocks.useCheckAvailabilityBuckets.mockReturnValue({
      data: bucketsResponse([cell(HOUR_MS, HOUR_MS, null)], null),
      isLoading: false,
    });

    render(
      <ResponseTimeChart org="acme" checkUid="c1" initialPeriod="day" />,
    );

    const header = screen.getByTestId(
      "response-time-chart-window-availability",
    );
    expect(header.textContent).toContain("No data");
    expect(header.textContent).not.toContain("100%");
    expect(header.getAttribute("data-status")).toBe("noData");
  });
});
