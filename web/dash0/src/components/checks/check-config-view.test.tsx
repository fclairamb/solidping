/**
 * @vitest-environment jsdom
 *
 * Component test for the read-only check config view: a list such as
 * expectedStatusCodes reads as chips, like the edit form, not as a bulleted
 * list (fclairamb/solidping-business#6).
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type { Check } from "@/api/hooks";
import { CheckConfigView } from "./check-config-view";

afterEach(cleanup);

function renderConfig(config: Record<string, unknown>) {
  render(<CheckConfigView check={{ type: "http", config } as unknown as Check} />);
}

describe("CheckConfigView", () => {
  it("shows expectedStatusCodes as one chip per code", () => {
    renderConfig({ url: "https://acme.com", expectedStatusCodes: ["200", "4XX"] });

    const chips = screen.getByTestId("check-config-expectedStatusCodes-chips");
    expect(Array.from(chips.children).map((c) => c.textContent)).toEqual(["200", "4XX"]);
    expect(chips.closest("dl")).not.toBeNull();
    expect(screen.getByTestId("check-config-view").querySelector("ul")).toBeNull();
  });

  it("keeps maps as key/value rows", () => {
    renderConfig({ env: { REGION: "eu" } });

    const block = screen.getByTestId("check-config-env");
    expect(block.textContent).toContain("REGION");
    expect(block.textContent).toContain("eu");
  });
});
