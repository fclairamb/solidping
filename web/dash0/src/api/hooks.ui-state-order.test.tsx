/**
 * @vitest-environment jsdom
 *
 * Writes to one ui-state key are whole-entry PUTs, so they must reach the
 * server in call order. The GitHub star prompt writes `{shownAt}` when it
 * appears and `{shownAt, outcome}` when it is dismissed; if the second PUT
 * overtook the first, the outcome was erased and the prompt came back on the
 * user's other devices.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { PropsWithChildren } from "react";

const mocks = vi.hoisted(() => ({ apiFetch: vi.fn() }));

vi.mock("./client", () => ({
  apiFetch: mocks.apiFetch,
  getToken: () => "test-token",
}));

const { useSetUiState } = await import("./hooks");

function wrapper() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  });
  return ({ children }: PropsWithChildren) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
}

afterEach(() => {
  mocks.apiFetch.mockReset();
});

describe("useSetUiState", () => {
  it("sends a second write to the same key only after the first has answered", async () => {
    const started: string[] = [];
    const pending: Array<() => void> = [];
    mocks.apiFetch.mockImplementation(
      (url: string, init: { body: string }) =>
        new Promise((resolve) => {
          started.push(`${url} ${init.body}`);
          pending.push(() => resolve({ value: JSON.parse(init.body) }));
        }),
    );

    const { result } = renderHook(() => useSetUiState("github-star"), {
      wrapper: wrapper(),
    });

    result.current.mutate({ shownAt: "t0" });
    result.current.mutate({ shownAt: "t0", outcome: "dismissed" });

    await waitFor(() => expect(started).toHaveLength(1));
    // The dismissal waits for the "shown" write instead of racing it.
    await new Promise((r) => setTimeout(r, 20));
    expect(started).toEqual([
      '/api/v1/me/ui-state/github-star {"shownAt":"t0"}',
    ]);

    pending[0]();
    await waitFor(() => expect(started).toHaveLength(2));
    expect(started[1]).toBe(
      '/api/v1/me/ui-state/github-star {"shownAt":"t0","outcome":"dismissed"}',
    );
    pending[1]();
  });

  it("does not hold back a write to another key", async () => {
    const started: string[] = [];
    mocks.apiFetch.mockImplementation(
      (url: string) =>
        new Promise(() => {
          started.push(url);
        }),
    );

    const w = wrapper();
    const a = renderHook(() => useSetUiState("github-star"), { wrapper: w });
    const b = renderHook(() => useSetUiState("onboarding.acme"), {
      wrapper: w,
    });

    a.result.current.mutate({ shownAt: "t0" });
    b.result.current.mutate({ dismissedAt: "t0" });

    await waitFor(() => expect(started).toHaveLength(2));
  });
});
