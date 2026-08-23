/**
 * @vitest-environment jsdom
 *
 * Pins spec 2026-08-22-04's cache-hit acceptance criterion: the check-detail
 * route derives its region set and duration stats from the SAME window the
 * chart draws, and does so by issuing the identical queries so react-query
 * serves them from cache. One request per tier, not one per tier per consumer.
 */
import type { PropsWithChildren } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mocks = vi.hoisted(() => ({ apiFetch: vi.fn() }));

vi.mock("./client", () => ({
  apiFetch: mocks.apiFetch,
  getToken: () => "test-token",
}));

const { useResultTiers } = await import("./hooks");

const TIERS = [
  {
    checkUid: "check-1",
    periodType: "hour,day",
    periodStartAfter: "2026-07-23T12:00:00.000Z",
    with: "durationMs,region",
    size: 1000,
  },
  {
    checkUid: "check-1",
    periodType: "raw",
    periodStartAfter: "2026-07-23T12:00:00.000Z",
    with: "durationMs,region",
    size: 1000,
  },
];

function Consumer({ label }: { label: string }) {
  const { data, isLoading } = useResultTiers("acme", TIERS);

  return (
    <div data-testid={label}>
      {isLoading ? "loading" : data.data.map((r) => r.uid).join(",")}
    </div>
  );
}

function wrapper(client: QueryClient) {
  return function Wrapper({ children }: PropsWithChildren) {
    return (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
  };
}

describe("useResultTiers", () => {
  beforeEach(() => {
    mocks.apiFetch.mockReset();
    mocks.apiFetch.mockImplementation(async (path: string) => {
      const periodType = new URL(path, "http://x").searchParams.get(
        "periodType",
      );

      return periodType === "raw"
        ? {
            data: [
              { uid: "r-2", periodStart: "2026-08-22T11:59:00.000Z" },
              { uid: "r-1", periodStart: "2026-08-22T11:58:00.000Z" },
            ],
            pagination: {},
          }
        : {
            data: [
              { uid: "h-2", periodStart: "2026-08-22T11:00:00.000Z" },
              { uid: "h-1", periodStart: "2026-08-22T10:00:00.000Z" },
            ],
            pagination: {},
          };
    });
  });

  afterEach(cleanup);

  it("issues one request per tier, shared across consumers of the same plan", async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });

    render(
      <>
        <Consumer label="chart" />
        <Consumer label="stats" />
      </>,
      { wrapper: wrapper(client) },
    );

    await waitFor(() =>
      expect(screen.getByTestId("chart").textContent).not.toBe("loading"),
    );
    await waitFor(() =>
      expect(screen.getByTestId("stats").textContent).not.toBe("loading"),
    );

    // Two tiers, two consumers, TWO requests — the second consumer is a cache
    // hit. Four would mean the two call sites diverged into separate fetches,
    // which is exactly the regression this asserts against.
    expect(mocks.apiFetch).toHaveBeenCalledTimes(2);

    const periodTypes = mocks.apiFetch.mock.calls.map((call) =>
      new URL(call[0] as string, "http://x").searchParams.get("periodType"),
    );
    expect(periodTypes.sort()).toEqual(["hour,day", "raw"]);

    // No issued query straddles the raw/rollup index split.
    for (const pt of periodTypes) {
      const tiers = (pt ?? "").split(",");
      expect(
        tiers.includes("raw") &&
          tiers.some((t) => ["hour", "day", "month"].includes(t)),
      ).toBe(false);
    }

    // Both consumers see the same merged, descending series.
    const expected = "r-2,r-1,h-2,h-1";
    expect(screen.getByTestId("chart").textContent).toBe(expected);
    expect(screen.getByTestId("stats").textContent).toBe(expected);
  });

  it("walks every cursor page of a tier before merging", async () => {
    mocks.apiFetch.mockReset();
    mocks.apiFetch.mockImplementation(async (path: string) => {
      const params = new URL(path, "http://x").searchParams;
      if (params.get("periodType") !== "raw") {
        return { data: [], pagination: {} };
      }

      return params.get("cursor")
        ? {
            data: [{ uid: "r-1", periodStart: "2026-08-22T11:58:00.000Z" }],
            pagination: {},
          }
        : {
            data: [{ uid: "r-2", periodStart: "2026-08-22T11:59:00.000Z" }],
            pagination: { cursor: "page-2" },
          };
    });

    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });

    render(<Consumer label="chart" />, { wrapper: wrapper(client) });

    await waitFor(() =>
      expect(screen.getByTestId("chart").textContent).toBe("r-2,r-1"),
    );
  });
});
