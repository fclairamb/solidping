/**
 * @vitest-environment jsdom
 *
 * Spec 2026-08-26-10: useCheckAvailabilityBuckets turns the chart's strip
 * query into the buckets endpoint's query string. The bucket width travels
 * as whole hours ("1h", "6h", "24h"), an omitted width lets the server pick,
 * and the region filter is forwarded only when one is selected.
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

const { useCheckAvailabilityBuckets } = await import("./hooks");

const FROM = "2026-10-01T00:00:00.000Z";
const TO = "2026-10-02T00:00:00.000Z";

function wrapper({ children }: PropsWithChildren) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

async function requestedParams(query: {
  bucketSeconds?: number;
  region?: string;
}): Promise<URLSearchParams> {
  renderHook(
    () =>
      useCheckAvailabilityBuckets("acme", "c1", {
        from: FROM,
        to: TO,
        ...query,
      }),
    { wrapper },
  );
  await waitFor(() => expect(mocks.apiFetch).toHaveBeenCalledTimes(1));
  const url = mocks.apiFetch.mock.calls[0][0] as string;
  expect(
    url.startsWith("/api/v1/orgs/acme/checks/c1/availability/buckets?"),
  ).toBe(true);
  return new URLSearchParams(url.split("?")[1]);
}

describe("useCheckAvailabilityBuckets query string", () => {
  beforeEach(() => {
    mocks.apiFetch.mockReset();
    mocks.apiFetch.mockResolvedValue({ data: [] });
  });

  afterEach(() => {
    cleanup();
  });

  it.each([
    [3600, "1h"],
    [6 * 3600, "6h"],
    [24 * 3600, "24h"],
  ])("sends a %is bucket as bucket=%s", async (seconds, expected) => {
    const params = await requestedParams({ bucketSeconds: seconds });
    expect(params.get("bucket")).toBe(expected);
    expect(params.get("from")).toBe(FROM);
    expect(params.get("to")).toBe(TO);
  });

  it("omits the bucket when the caller leaves the width to the server", async () => {
    const params = await requestedParams({});
    expect(params.has("bucket")).toBe(false);
  });

  it("forwards the region filter only when one is selected", async () => {
    expect((await requestedParams({ region: "us-east" })).get("region")).toBe(
      "us-east",
    );
    cleanup();
    mocks.apiFetch.mockClear();
    expect((await requestedParams({})).has("region")).toBe(false);
  });
});
