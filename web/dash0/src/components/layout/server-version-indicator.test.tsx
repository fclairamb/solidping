/**
 * @vitest-environment jsdom
 *
 * Component tests for the sidebar server-version indicator (spec
 * 2026-08-28-01): the muted normal state, the red reload affordance that
 * appears once the polled server version diverges from the boot-time
 * baseline this page loaded with, and that clicking it reloads the page.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import "@/i18n";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ServerVersionIndicator } from "./server-version-indicator";
import { resetLoadedServerVersionForTests } from "@/lib/server-version";

const mocks = vi.hoisted(() => ({ apiFetch: vi.fn() }));

vi.mock("@/api/client", () => ({
  apiFetch: mocks.apiFetch,
  getToken: () => null,
}));

/**
 * Routes by call shape rather than call order: `getLoadedServerVersion`
 * always passes `{ skipAuth: true }` (the one-shot boot fetch), while
 * `useVersion`'s poll calls `apiFetch` with no options — the two fire from
 * independent effects and their relative timing isn't something a test
 * should depend on.
 */
function mockVersionEndpoint(loaded: string, initialCurrent: string) {
  let current = initialCurrent;
  mocks.apiFetch.mockImplementation(
    async (_url: string, opts?: { skipAuth?: boolean }) => ({
      version: opts?.skipAuth ? loaded : current,
    }),
  );
  return {
    redeploy(version: string) {
      current = version;
    },
  };
}

function renderIndicator() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const result = render(
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <ServerVersionIndicator />
      </TooltipProvider>
    </QueryClientProvider>,
  );
  return { client, ...result };
}

describe("ServerVersionIndicator", () => {
  beforeEach(() => {
    mocks.apiFetch.mockReset();
    resetLoadedServerVersionForTests();
  });

  afterEach(() => {
    cleanup();
  });

  it("renders the current version, muted, when the client matches the server", async () => {
    mockVersionEndpoint("1.2.3", "1.2.3");
    renderIndicator();

    await waitFor(() =>
      expect(screen.getByTestId("server-version-text").textContent).toBe("v1.2.3"),
    );
    expect(screen.queryByTestId("server-version-reload")).toBeNull();
  });

  it("renders 'dev' without a v-prefix and never flags dev-vs-dev as stale", async () => {
    mockVersionEndpoint("dev", "dev");
    renderIndicator();

    await waitFor(() =>
      expect(screen.getByTestId("server-version-text").textContent).toBe("dev"),
    );
    expect(screen.queryByTestId("server-version-reload")).toBeNull();
  });

  it("shows a red reload icon once a redeploy changes the polled version", async () => {
    const endpoint = mockVersionEndpoint("1.2.3", "1.2.3");
    const { client } = renderIndicator();

    await waitFor(() =>
      expect(screen.getByTestId("server-version-text").textContent).toBe("v1.2.3"),
    );
    expect(screen.queryByTestId("server-version-reload")).toBeNull();

    // Simulate a redeploy: the server now answers with a newer version.
    // Production re-fetches this on the 15-minute interval and on window
    // focus/visibilitychange; here we force the same query to refetch.
    endpoint.redeploy("1.2.4");
    await client.invalidateQueries({ queryKey: ["version"] });

    await waitFor(() =>
      expect(screen.getByTestId("server-version-reload")).toBeTruthy(),
    );
    // The muted label now shows the OLD (loaded) version — that's the one
    // actually running in this tab — not the new server version.
    expect(screen.getByTestId("server-version-text").textContent).toBe("v1.2.3");
    expect(
      screen.getByTestId("server-version-reload").className,
    ).toContain("text-destructive");
  });

  it("reloads the page when the reload icon is clicked", async () => {
    const endpoint = mockVersionEndpoint("1.2.3", "1.2.3");
    const { client } = renderIndicator();
    await waitFor(() =>
      expect(screen.getByTestId("server-version-text").textContent).toBe("v1.2.3"),
    );

    endpoint.redeploy("1.2.4");
    await client.invalidateQueries({ queryKey: ["version"] });
    await waitFor(() =>
      expect(screen.getByTestId("server-version-reload")).toBeTruthy(),
    );

    const reload = vi.fn();
    Object.defineProperty(window, "location", {
      configurable: true,
      value: { ...window.location, reload },
    });

    screen.getByTestId("server-version-reload").click();

    expect(reload).toHaveBeenCalledTimes(1);
  });
});
