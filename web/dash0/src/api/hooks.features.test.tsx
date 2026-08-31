/**
 * @vitest-environment jsdom
 *
 * Spec 2026-08-29-12: `useFeatures` gained an `enabled` option so `OrgLayout`
 * can skip `GET /api/v1/features` on the public login/register routes, where
 * there is no token and the (authenticated) endpoint would 401 — which used
 * to bounce an unauthenticated visitor off `/register` with a spurious
 * "session expired" redirect. Assert directly on the REQUEST: `enabled: false`
 * must issue no call at all, paired with a positive control proving the
 * default (and an explicit `enabled: true`) still fetches normally.
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

const { useFeatures } = await import("./hooks");

function wrapper({ children }: PropsWithChildren) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function Probe({ enabled }: { enabled?: boolean }) {
  const { data, isFetching } = useFeatures(
    enabled === undefined ? undefined : { enabled },
  );
  return (
    <div data-testid="probe" data-fetching={isFetching}>
      {data ? "loaded" : "empty"}
    </div>
  );
}

describe("useFeatures enabled gating", () => {
  beforeEach(() => {
    mocks.apiFetch.mockReset();
    mocks.apiFetch.mockResolvedValue({ bugReport: true });
  });

  afterEach(() => {
    cleanup();
  });

  it("issues no request when enabled is false", async () => {
    render(<Probe enabled={false} />, { wrapper });

    // Give any (wrongly-)issued request a chance to land before asserting
    // the negative — a bare synchronous check would pass even if the query
    // fired and merely hadn't resolved yet.
    await new Promise((resolve) => setTimeout(resolve, 50));

    expect(mocks.apiFetch).not.toHaveBeenCalled();
    expect(screen.getByTestId("probe").textContent).toBe("empty");
  });

  it("fetches normally when enabled is true", async () => {
    render(<Probe enabled={true} />, { wrapper });

    await waitFor(() => expect(screen.getByTestId("probe").textContent).toBe("loaded"));
    expect(mocks.apiFetch).toHaveBeenCalledWith("/api/v1/features");
  });

  it("fetches normally when no options are passed (default enabled)", async () => {
    render(<Probe />, { wrapper });

    await waitFor(() => expect(screen.getByTestId("probe").textContent).toBe("loaded"));
    expect(mocks.apiFetch).toHaveBeenCalledWith("/api/v1/features");
  });
});
