/**
 * @vitest-environment jsdom
 *
 * Issue #1023: the ntfy form had no access-token field although the sender
 * (server/internal/notifications/ntfy.go) and the docs support one. The
 * token must be editable and land in settings.auth_token, the key the
 * credentials registry encrypts.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { IntegrationForm } from "./integration-form";

describe("IntegrationForm ntfy", () => {
  afterEach(() => cleanup());

  it("exposes an access token field written to settings.auth_token", async () => {
    const onChange = vi.fn();
    render(
      <QueryClientProvider client={new QueryClient()}>
        <IntegrationForm type="ntfy" initial={null} onChange={onChange} />
      </QueryClientProvider>,
    );

    const token = await screen.findByTestId("ch-ntfy-token");
    expect(token).toHaveProperty("type", "password");
    fireEvent.change(token, { target: { value: "tk_secret" } });

    const last = onChange.mock.calls.at(-1)?.[0] as { settings: Record<string, unknown> };
    expect(last.settings.auth_token).toBe("tk_secret");
  });
});
