/**
 * @vitest-environment jsdom
 *
 * Spec 2026-09-29-05: bugReport, heartbeat, runMode and deploymentMode moved
 * from /api/v1/features and /api/mgmt/version into the public config
 * document. Each hook must answer "off / undefined" while loading and the
 * server's value once loaded.
 */
import type { PropsWithChildren } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import {
  useBugReportEnabled,
  useDeploymentMode,
  useHeartbeatPush,
  useRunMode,
} from "./public-config";

function wrapper({ children }: PropsWithChildren) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function Probe() {
  const bug = useBugReportEnabled();
  const push = useHeartbeatPush();
  const deployment = useDeploymentMode();
  const runMode = useRunMode();
  return (
    <div data-testid="probe">
      {JSON.stringify({ bug, push, deployment, runMode })}
    </div>
  );
}

const probe = () => JSON.parse(screen.getByTestId("probe").textContent ?? "{}");

describe("public-config hooks", () => {
  let resolveFetch: (value: Response) => void;

  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            resolveFetch = resolve;
          }),
      ),
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("defaults to off / undefined while loading", () => {
    render(<Probe />, { wrapper });

    expect(probe()).toEqual({ bug: false });
    expect(fetch).toHaveBeenCalledWith("/api/v1/config", expect.anything());
  });

  it("returns the server values once loaded", async () => {
    render(<Probe />, { wrapper });

    const heartbeat = {
      tcpEnabled: true,
      udpEnabled: false,
      host: "acme.example.com",
      tcpPort: 4001,
      udpPort: 0,
    };
    resolveFetch(
      new Response(
        JSON.stringify({
          bugReport: { enabled: true },
          heartbeat,
          runMode: "test",
          deploymentMode: "saas",
        }),
        { status: 200 },
      ),
    );

    await waitFor(() => expect(probe().bug).toBe(true));
    expect(probe()).toEqual({
      bug: true,
      push: heartbeat,
      deployment: "saas",
      runMode: "test",
    });
  });
});
