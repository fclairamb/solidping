/**
 * @vitest-environment jsdom
 *
 * Spec 2026-10-28-08: the Full range switch flips exactly once per click (on
 * the switch or on its label text), is labeled, and fullRangeHasEffect tells
 * whether pinning the domain changes anything.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { TooltipProvider } from "@/components/ui/tooltip";
import "@/i18n";

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

const mocks = vi.hoisted(() => ({
  useChartWindowResults: vi.fn(),
  useRegions: vi.fn(() => ({ data: { regions: [] } })),
  useCheckAvailabilityBuckets: vi.fn(() => ({ data: undefined, isLoading: false })),
}));

vi.mock("@/api/hooks", () => ({
  useChartWindowResults: mocks.useChartWindowResults,
  useRegions: mocks.useRegions,
  useCheckAvailabilityBuckets: mocks.useCheckAvailabilityBuckets,
}));
vi.mock("@/components/checks/pinned-result-box", () => ({
  PinnedResultBox: () => null,
}));

const { ResponseTimeChart, fullRangeHasEffect } = await import(
  "./response-time-chart"
);

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("fullRangeHasEffect", () => {
  const start = 0;
  const end = 3_600_000;
  const minutely = Array.from({ length: 61 }, (_, i) => i * 60_000);

  it("is false when the data already spans the window", () => {
    expect(fullRangeHasEffect(minutely, start, end)).toBe(false);
  });
  it("is true when there is a gap at an edge", () => {
    expect(fullRangeHasEffect(minutely.slice(10), start, end)).toBe(true);
    expect(fullRangeHasEffect(minutely.slice(0, 30), start, end)).toBe(true);
  });
  it("is true with no data", () => {
    expect(fullRangeHasEffect([], start, end)).toBe(true);
  });
});

describe("Full range switch", () => {
  it("flips once per click on the switch and once per click on its label", () => {
    mocks.useChartWindowResults.mockReturnValue({
      data: {
        data: [
          {
            uid: "r1",
            region: "eu",
            periodType: "raw",
            periodStart: new Date(Date.now() - 600_000).toISOString(),
            durationMs: 100,
            status: "up",
          },
        ],
      },
      isLoading: false,
      rawError: null,
      isEmptyPending: false,
    });
    render(
      <TooltipProvider>
        <ResponseTimeChart org="acme" checkUid="c1" initialPeriod="hour" />
      </TooltipProvider>,
    );
    const sw = screen.getByTestId("response-time-chart-full-range-toggle");
    expect(sw.getAttribute("aria-label")).toBeTruthy();
    const before = sw.getAttribute("data-state");
    fireEvent.click(sw);
    const afterSwitch = sw.getAttribute("data-state");
    expect(afterSwitch).not.toBe(before);
    fireEvent.click(screen.getByText(sw.getAttribute("aria-label")!, { selector: "span" }));
    expect(sw.getAttribute("data-state")).toBe(before);
  });
});
