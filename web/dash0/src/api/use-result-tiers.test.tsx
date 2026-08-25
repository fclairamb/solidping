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

function Consumer({
  label,
  tiers = TIERS,
}: {
  label: string;
  tiers?: typeof TIERS;
}) {
  const { data, isLoading } = useResultTiers("acme", tiers);

  return (
    <div data-testid={label}>
      {isLoading ? "loading" : data.data.map((r) => r.uid).join(",")}
    </div>
  );
}

/** Mirrors the check-detail route, which feeds `chartWindowResults` straight
 * into observedRegions with NO loading guard — so a stale merge is visible to
 * the user there even while the new check's tiers are still in flight. */
function UnguardedConsumer({
  label,
  tiers,
}: {
  label: string;
  tiers: typeof TIERS;
}) {
  const { data } = useResultTiers("acme", tiers);

  return <div data-testid={label}>{data.data.map((r) => r.uid).join(",")}</div>;
}

function tiersFor(checkUid: string): typeof TIERS {
  return TIERS.map((t) => ({ ...t, checkUid }));
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

  // The merged array is produced by a useMemo whose dependency is a string
  // signature, because a variable-length deps array is a hook-rules violation
  // (the tier count changes with the range). That signature has to name every
  // input that changes which rows belong in the merge — org and checkUid
  // included. Two checks visited close together can hold cache entries with the
  // same `dataUpdatedAt` millisecond; if the signature only covered the window,
  // navigating between them would return the PREVIOUS check's rows.
  it("does not surface the previous check's rows when only checkUid changes", async () => {
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false, staleTime: Infinity, refetchOnMount: false },
      },
    });

    // Identical updatedAt across BOTH checks — the collision the signature has
    // to survive without leaning on dataUpdatedAt.
    const updatedAt = 1_700_000_000_000;
    for (const checkUid of ["check-1", "check-2"]) {
      for (const tier of tiersFor(checkUid)) {
        client.setQueryData(
          ["allResults", "acme", tier],
          {
            data: [
              {
                uid: `${checkUid}-${tier.periodType === "raw" ? "raw" : "roll"}`,
                periodStart:
                  tier.periodType === "raw"
                    ? "2026-08-22T11:59:00.000Z"
                    : "2026-08-22T11:00:00.000Z",
              },
            ],
          },
          { updatedAt },
        );
      }
    }

    const { rerender } = render(
      <UnguardedConsumer label="stats" tiers={tiersFor("check-1")} />,
      { wrapper: wrapper(client) },
    );

    await waitFor(() =>
      expect(screen.getByTestId("stats").textContent).toBe(
        "check-1-raw,check-1-roll",
      ),
    );

    rerender(<UnguardedConsumer label="stats" tiers={tiersFor("check-2")} />);

    await waitFor(() =>
      expect(screen.getByTestId("stats").textContent).toBe(
        "check-2-raw,check-2-roll",
      ),
    );
    expect(screen.getByTestId("stats").textContent).not.toContain("check-1");
  });

  // Same argument for the org: an org switch that keeps the window and the
  // check identifier must not re-serve the previous org's rows.
  it("does not surface another org's rows when only org changes", async () => {
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false, staleTime: Infinity, refetchOnMount: false },
      },
    });

    const updatedAt = 1_700_000_000_000;
    for (const org of ["acme", "other"]) {
      for (const tier of TIERS) {
        client.setQueryData(
          ["allResults", org, tier],
          {
            data: [
              {
                uid: `${org}-${tier.periodType === "raw" ? "raw" : "roll"}`,
                periodStart:
                  tier.periodType === "raw"
                    ? "2026-08-22T11:59:00.000Z"
                    : "2026-08-22T11:00:00.000Z",
              },
            ],
          },
          { updatedAt },
        );
      }
    }

    function OrgConsumer({ org }: { org: string }) {
      const { data } = useResultTiers(org, TIERS);

      return (
        <div data-testid="org">{data.data.map((r) => r.uid).join(",")}</div>
      );
    }

    const { rerender } = render(<OrgConsumer org="acme" />, {
      wrapper: wrapper(client),
    });

    await waitFor(() =>
      expect(screen.getByTestId("org").textContent).toBe("acme-raw,acme-roll"),
    );

    rerender(<OrgConsumer org="other" />);

    await waitFor(() =>
      expect(screen.getByTestId("org").textContent).toBe(
        "other-raw,other-roll",
      ),
    );
  });
});
