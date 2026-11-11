/**
 * @vitest-environment jsdom
 *
 * The check page's Recent Results card can list failed runs only: useResults
 * forwards `status` to GET /results (where "down" also matches timeout and
 * error), and leaves it out when no status filter is set.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { PropsWithChildren } from "react";

const mocks = vi.hoisted(() => ({ apiFetch: vi.fn() }));

vi.mock("./client", () => ({
  apiFetch: mocks.apiFetch,
  getToken: () => "test-token",
}));

const { useResults } = await import("./hooks");

function wrapper({ children }: PropsWithChildren) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

async function requestedParams(status?: string): Promise<URLSearchParams> {
  renderHook(() => useResults("acme", { checkUid: "c1", size: 10, status }), { wrapper });
  await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(1));
  const url = mocks.apiFetch.mock.calls[0][0] as string;
  expect(url.startsWith("/api/v1/orgs/acme/results?")).toBe(true);
  return new URLSearchParams(url.split("?")[1]);
}

describe("useResults status filter", () => {
  beforeEach(() => {
    mocks.apiFetch.mockReset();
    mocks.apiFetch.mockResolvedValue({ data: [] });
  });

  afterEach(() => cleanup());

  it("forwards status=down", async () => {
    const params = await requestedParams("down");
    expect(params.get("status")).toBe("down");
    expect(params.get("checkUid")).toBe("c1");
  });

  it("omits status when no filter is set", async () => {
    const params = await requestedParams(undefined);
    expect(params.has("status")).toBe(false);
  });
});
