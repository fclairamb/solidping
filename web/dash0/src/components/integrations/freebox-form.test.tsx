/**
 * @vitest-environment jsdom
 *
 * Spec 2026-09-29-05: the Freebox form takes its SaaS branch (base URL field
 * disabled) when the public config says deploymentMode "saas", and stays
 * editable otherwise.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("@/api/hooks", () => ({
  useStartFreeboxPairing: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useFreeboxPairingStatus: () => ({ data: undefined }),
}));

import { FreeboxForm } from "./freebox-form";

function renderForm(config: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify(config), { status: 200 })),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <FreeboxForm org="acme" onPaired={vi.fn()} onCancel={vi.fn()} />
    </QueryClientProvider>,
  );
}

describe("FreeboxForm deployment mode", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("disables the base URL field when deploymentMode is saas", async () => {
    renderForm({ deploymentMode: "saas" });
    await waitFor(() =>
      expect(screen.getByTestId("freebox-url")).toHaveProperty("disabled", true),
    );
    expect(screen.getByText(/Custom base URLs aren't available/)).toBeTruthy();
  });

  it("keeps the base URL field editable when deploymentMode is not saas", async () => {
    renderForm({ deploymentMode: "self-hosted" });
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    await waitFor(() =>
      expect(screen.getByText(/Leave as-is if SolidPing runs/)).toBeTruthy(),
    );
    expect(screen.getByTestId("freebox-url")).toHaveProperty("disabled", false);
    expect(screen.queryByText(/Custom base URLs aren't available/)).toBeNull();
  });
});
