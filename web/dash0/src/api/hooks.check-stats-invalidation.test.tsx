/**
 * @vitest-environment jsdom
 *
 * Spec 2026-09-01-01 layer 2: the dashboard's empty-org hero is gated on
 * useCheckStats (query key ["check-stats", org]), but useCreateCheck and
 * useDeleteCheck used to invalidate only ["checks", org] and
 * ["checks", "infinite", org] — so the gate query kept serving its own
 * cached/polled value for up to five minutes after a check was created or
 * deleted. This asserts the fix directly on the query client: both
 * mutations must invalidate ["check-stats", org] too.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { PropsWithChildren } from "react";

const mocks = vi.hoisted(() => ({ apiFetch: vi.fn() }));

vi.mock("./client", () => ({
  apiFetch: mocks.apiFetch,
  getToken: () => "test-token",
}));

const { useCreateCheck, useDeleteCheck } = await import("./hooks");

const ORG = "acme";

function makeWrapper(client: QueryClient) {
  return function Wrapper({ children }: PropsWithChildren) {
    return (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
  };
}

describe("check-stats cache invalidation on check create/delete", () => {
  beforeEach(() => {
    mocks.apiFetch.mockReset();
  });

  afterEach(() => {
    cleanup();
  });

  it("useCreateCheck invalidates check-stats alongside the checks list", async () => {
    mocks.apiFetch.mockResolvedValue({ uid: "chk_1" });

    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");

    const { result } = renderHook(() => useCreateCheck(ORG), {
      wrapper: makeWrapper(client),
    });

    await act(async () => {
      result.current.mutate({ type: "http", config: { url: "https://example.com" } });
    });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    const invalidatedKeys = invalidateSpy.mock.calls.map(
      (call) => call[0]?.queryKey,
    );

    expect(invalidatedKeys).toContainEqual(["checks", ORG]);
    expect(invalidatedKeys).toContainEqual(["checks", "infinite", ORG]);
    expect(invalidatedKeys).toContainEqual(["check-stats", ORG]);
  });

  it("useDeleteCheck invalidates check-stats alongside the checks list", async () => {
    mocks.apiFetch.mockResolvedValue(undefined);

    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");

    const { result } = renderHook(() => useDeleteCheck(ORG), {
      wrapper: makeWrapper(client),
    });

    await act(async () => {
      result.current.mutate("chk_1");
    });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    const invalidatedKeys = invalidateSpy.mock.calls.map(
      (call) => call[0]?.queryKey,
    );

    expect(invalidatedKeys).toContainEqual(["checks", ORG]);
    expect(invalidatedKeys).toContainEqual(["checks", "infinite", ORG]);
    expect(invalidatedKeys).toContainEqual(["check-stats", ORG]);
  });
});
