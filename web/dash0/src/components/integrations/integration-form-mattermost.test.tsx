/**
 * @vitest-environment jsdom
 *
 * The Mattermost form must expose the blocks switch and write the value the
 * sender reads: settings.message_format = "mm_blocks".
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { IntegrationForm } from "./integration-form";

describe("IntegrationForm Mattermost", () => {
  afterEach(() => cleanup());

  it("exposes a Mattermost Blocks switch written to settings.message_format", async () => {
    const onChange = vi.fn();
    render(
      <QueryClientProvider client={new QueryClient()}>
        <IntegrationForm type="mattermost" initial={null} onChange={onChange} />
      </QueryClientProvider>,
    );

    const toggle = await screen.findByTestId("mattermost-use-blocks");
    fireEvent.click(toggle);

    const last = onChange.mock.calls.at(-1)?.[0] as { settings: Record<string, unknown> };
    expect(last.settings.message_format).toBe("mm_blocks");
  });
});
