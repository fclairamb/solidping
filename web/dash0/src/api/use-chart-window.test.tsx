/**
 * @vitest-environment jsdom
 *
 * Spec 2026-08-22-07, asserted where the spec says to assert it: on the
 * REQUEST PARAMETERS the chart issues. A chart that renders "no data" satisfies
 * most naive assertions about points, so every negative here is paired with a
 * control proving the ordinary case does fetch.
 */
import type { PropsWithChildren } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mocks = vi.hoisted(() => ({ apiFetch: vi.fn() }));

vi.mock("./client", () => ({
  apiFetch: mocks.apiFetch,
  getToken: () => "test-token",
}));

const { useChartWindowResults } = await import("./hooks");
const { getStartFor } = await import("@/lib/chart-window");

const ONE_MINUTE = 60_000;
const ONE_HOUR = 3_600_000;
const REGIONS = ["eu", "us", "ap"];

/** Every request the hook issued, as parsed query params. */
let issued: URLSearchParams[] = [];

function paramsOf(path: string): URLSearchParams {
  return new URL(path, "http://x").searchParams;
}

function rawRequests(): URLSearchParams[] {
  return issued.filter((p) => p.get("periodType") === "raw");
}

function rollupRequests(): URLSearchParams[] {
  return issued.filter((p) => p.get("periodType") !== "raw");
}

/** Hourly rollup rows for three regions, newest bucket `lagHours` back. Rows
 * carry `periodEnd` exactly as the API sends them for aggregated tiers — the
 * seam anchors on that exclusive edge. */
function rollupRows(lagHours: number, buckets = 24) {
  const newest = Math.floor(Date.now() / ONE_HOUR) * ONE_HOUR - lagHours * ONE_HOUR;

  return REGIONS.flatMap((region) =>
    Array.from({ length: buckets }, (_, i) => {
      const start = newest - i * ONE_HOUR;

      return {
        uid: `h-${region}-${i}`,
        region,
        periodType: "hour",
        periodStart: new Date(start).toISOString(),
        periodEnd: new Date(start + ONE_HOUR).toISOString(),
        durationMs: 100,
      };
    }),
  );
}

/** The seam the fixture implies: the newest bucket's exclusive upper edge. */
function expectedSeam(lagHours: number): string {
  return rollupRows(lagHours)[0].periodEnd;
}

function rawRows() {
  return REGIONS.map((region) => ({
    uid: `r-${region}`,
    region,
    periodType: "raw",
    periodStart: new Date(Date.now() - ONE_MINUTE).toISOString(),
    durationMs: 120,
  }));
}

function Consumer({
  rawRefetchInterval,
  periodMs,
}: {
  rawRefetchInterval?: number;
  /** Deliberately NOT defaulted: the check-detail page really does render this
   * hook with `undefined` until useCheck resolves the check's period. */
  periodMs?: number;
}) {
  const { data, isLoading, rawError, rawPending } = useChartWindowResults(
    "acme",
    "check-1",
    { timeRange: "month", periodMs },
    { rawRefetchInterval },
  );

  return (
    <>
      <div data-testid="loading">{String(isLoading)}</div>
      <div data-testid="rawPending">{String(rawPending)}</div>
      <div data-testid="rawError">{rawError ? "error" : "none"}</div>
      <div data-testid="uids">{data.data.map((r) => r.uid).join(",")}</div>
    </>
  );
}

/** The check-detail route's own call site: the SAME window, a zoom object it
 * memoizes itself, and its own (faster) poll cadence. It feeds the result
 * straight into observedRegions with no loading guard. */
function RouteConsumer() {
  const { data } = useChartWindowResults(
    "acme",
    "check-1",
    { timeRange: "month", periodMs: ONE_MINUTE, zoom: undefined },
    { rawRefetchInterval: ONE_MINUTE },
  );

  return (
    <div data-testid="route-uids">{data.data.map((r) => r.uid).join(",")}</div>
  );
}

function wrapper(client: QueryClient) {
  return function Wrapper({ children }: PropsWithChildren) {
    return (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
  };
}

function newClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
}

/** Serves rollups from `lagHours` back and raw always; the default fixture. */
function serve(lagHours: number, opts?: { rollup?: unknown[]; raw?: () => Promise<unknown> }) {
  mocks.apiFetch.mockImplementation(async (path: string) => {
    const params = paramsOf(path);
    issued.push(params);

    if (params.get("periodType") === "raw") {
      if (opts?.raw) return opts.raw();

      return { data: rawRows(), pagination: {} };
    }

    return { data: opts?.rollup ?? rollupRows(lagHours), pagination: {} };
  });
}

describe("useChartWindowResults", () => {
  beforeEach(() => {
    issued = [];
    mocks.apiFetch.mockReset();
  });

  afterEach(cleanup);

  it("issues one rollup page and one raw page, with raw bounded to the seam", async () => {
    serve(1);
    render(<Consumer periodMs={ONE_MINUTE} />, { wrapper: wrapper(newClient()) });

    await waitFor(() => expect(rawRequests()).toHaveLength(1));
    await waitFor(() =>
      expect(screen.getByTestId("uids").textContent).toContain("r-eu"),
    );

    expect(rollupRequests()).toHaveLength(1);
    expect(rawRequests()).toHaveLength(1);
    expect(rollupRequests()[0].get("periodType")).toBe("hour,day");

    const rollupStart = rollupRequests()[0].get("periodStartAfter")!;
    const rawStart = rawRequests()[0].get("periodStartAfter")!;

    // The rollup pass still covers the whole month…
    expect(rollupStart).toBe(getStartFor("month"));
    // …while raw covers only the seam: the end of the newest bucket pass 1
    // returned, so the two tiers cannot both cover that bucket.
    expect(rawStart).toBe(expectedSeam(1));

    // Quantified: the seam is a couple of hours, not the 24 h raw-retention
    // window the chart used to ask for on every wide range.
    const seamMs = Date.now() - Date.parse(rawStart);
    expect(seamMs).toBeLessThan(3 * ONE_HOUR);
    expect(Date.now() - Date.parse(rollupStart)).toBeGreaterThan(29 * 24 * ONE_HOUR);

    // ≤ 1 page each: 3 regions × ~120 raw points fits one 1000-row page, which
    // is what turns five sequential round-trips into one.
    expect((seamMs / ONE_MINUTE) * REGIONS.length).toBeLessThan(1000);
    expect(rawRequests()[0].get("limit")).toBe("1000");
  });

  it("widens the seam when the aggregator is lagging", async () => {
    // The case a hard-coded 'last hour' window gets wrong: rollups six hours
    // stale must make raw cover six hours, or the chart's right edge is empty.
    serve(6);
    render(<Consumer periodMs={ONE_MINUTE} />, { wrapper: wrapper(newClient()) });

    await waitFor(() => expect(rawRequests()).toHaveLength(1));

    const rawStart = Date.parse(rawRequests()[0].get("periodStartAfter")!);
    const seamHours = (Date.now() - rawStart) / ONE_HOUR;

    expect(seamHours).toBeGreaterThan(4.9);
    expect(seamHours).toBeLessThan(6.1);
    expect(rawRequests()[0].get("periodStartAfter")).toBe(expectedSeam(6));
  });

  it("fetches raw over the whole window when pass 1 returns nothing", async () => {
    // A check younger than one rollup bucket has raw and nothing else. This is
    // also the control that raw IS fetched and its rows do reach the series.
    serve(1, { rollup: [] });
    render(<Consumer periodMs={ONE_MINUTE} />, { wrapper: wrapper(newClient()) });

    await waitFor(() => expect(rawRequests()).toHaveLength(1));
    await waitFor(() =>
      expect(screen.getByTestId("uids").textContent).toBe("r-us,r-eu,r-ap"),
    );

    expect(rawRequests()[0].get("periodStartAfter")).toBe(getStartFor("month"));
  });

  it("renders from pass-1 data while pass 2 is still in flight", async () => {
    serve(1, { raw: () => new Promise(() => {}) });
    render(<Consumer periodMs={ONE_MINUTE} />, { wrapper: wrapper(newClient()) });

    // isLoading (the skeleton) belongs to pass 1 only.
    await waitFor(() =>
      expect(screen.getByTestId("loading").textContent).toBe("false"),
    );
    expect(screen.getByTestId("rawPending").textContent).toBe("true");
    // Assert on the rows actually available to render, not on a flag.
    expect(screen.getByTestId("uids").textContent).toContain("h-eu-0");
  });

  it("keeps pass-1 data and surfaces the failure when pass 2 rejects", async () => {
    serve(1, { raw: () => Promise.reject(new Error("raw boom")) });
    render(<Consumer periodMs={ONE_MINUTE} />, { wrapper: wrapper(newClient()) });

    await waitFor(() =>
      expect(screen.getByTestId("rawError").textContent).toBe("error"),
    );
    expect(screen.getByTestId("loading").textContent).toBe("false");
    expect(screen.getByTestId("uids").textContent).toContain("h-eu-0");
  });

  it("serves the chart and the check-detail route from one request per tier", async () => {
    // Spec 2026-08-22-04's cache-hit criterion, restated for the two-pass hook:
    // one page-load, one window, one HTTP request per tier — no matter how many
    // components draw from it. The seam is now an input to the raw key, and the
    // two hook instances derive it independently, so this has to be asserted at
    // THIS level; the spec-04 test only covers a hand-written tier list.
    serve(1);
    render(
      <>
        <Consumer periodMs={ONE_MINUTE} rawRefetchInterval={5 * ONE_MINUTE} />
        <RouteConsumer />
      </>,
      { wrapper: wrapper(newClient()) },
    );

    await waitFor(() => expect(rawRequests()).toHaveLength(1));
    await waitFor(() =>
      expect(screen.getByTestId("route-uids").textContent).toContain("r-eu"),
    );

    expect(mocks.apiFetch).toHaveBeenCalledTimes(2);
    expect(rollupRequests()).toHaveLength(1);
    expect(rawRequests()).toHaveLength(1);
    // Both consumers drew the same merged series — the cache hit is real, not
    // one consumer quietly rendering nothing.
    expect(screen.getByTestId("route-uids").textContent).toBe(
      screen.getByTestId("uids").textContent,
    );
  });

  it("resolves the window at mount, so a later re-render cannot re-key the fetch", async () => {
    // The check-detail page hands this hook `periodMs` LATE: it comes from
    // parsePeriodMs(check?.period), so the first renders pass undefined and a
    // number arrives once useCheck resolves. The window start is
    // startOfMinute(now), so if the plan re-derived that bound on any such
    // re-render, a minute tick in between would silently re-key the raw query
    // and issue a second request for the same window — and would desynchronise
    // the chart from the route, which is the cache hit asserted above.
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date(Date.UTC(2026, 7, 22, 12, 0, 59, 500)));

    try {
      // Brand-new check: no seam, so the raw window IS the window bound — the
      // branch where a re-derived bound is observable on the wire.
      serve(1, { rollup: [] });

      const view = render(<Consumer periodMs={undefined} />, {
        wrapper: wrapper(newClient()),
      });
      await waitFor(() => expect(rawRequests()).toHaveLength(1));

      const windowStart = rawRequests()[0].get("periodStartAfter");
      expect(windowStart).toBe(
        new Date(Date.UTC(2026, 6, 23, 12, 0, 0)).toISOString(),
      );

      // A minute passes, then the check's period arrives and re-renders.
      vi.setSystemTime(new Date(Date.UTC(2026, 7, 22, 12, 1, 1, 0)));
      view.rerender(<Consumer periodMs={ONE_MINUTE} />);
      await waitFor(() =>
        expect(screen.getByTestId("uids").textContent).toContain("r-eu"),
      );
      // Give a re-keyed query time to fire before concluding none did — the
      // assertion below is a negative, so it needs the room to be wrong.
      await act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 50));
      });

      expect(rawRequests()).toHaveLength(1);
      expect(rollupRequests()).toHaveLength(1);
      expect(rawRequests()[0].get("periodStartAfter")).toBe(windowStart);
    } finally {
      vi.useRealTimers();
    }
  });

  it("re-fetches the seam on a poll tick and leaves the rollup pass alone", async () => {
    // Spec §3, and the assertion without which the optimisation silently
    // regresses to re-downloading a month of settled buckets every minute.
    serve(1);
    render(<Consumer periodMs={ONE_MINUTE} rawRefetchInterval={60} />, {
      wrapper: wrapper(newClient()),
    });

    await waitFor(() => expect(rawRequests().length).toBeGreaterThanOrEqual(3), {
      timeout: 3000,
    });
    expect(rollupRequests()).toHaveLength(1);
  });
});
