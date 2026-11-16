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
    renderConfig({
      url: "https://acme.com",
      expectedStatusCodes: ["200", "4XX"],
    });

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

  // Spec 2026-11-16-02: the server names the credential keys sent in clear
  // (redactedConfigKeys); their value renders under ph-no-capture, the other
  // rows do not.
  function renderCheck(
    type: string,
    config: Record<string, unknown>,
    redactedConfigKeys: string[],
  ) {
    render(<CheckConfigView check={{ type, config, redactedConfigKeys } as unknown as Check} />);
  }

  function blocked(testId: string): boolean {
    return screen.getByTestId(testId).querySelector(".ph-no-capture") !== null;
  }

  it("keeps a heartbeat token out of the replay, not its period", () => {
    renderCheck("heartbeat", { token: "hb-secret-token", period: "00:01:00" }, ["token"]);

    const token = screen.getByTestId("check-config-token-value");
    expect(token.textContent).toBe("hb-secret-token");
    expect(screen.getByText("hb-secret-token").closest(".ph-no-capture")).not.toBeNull();
    expect(blocked("check-config-token-value")).toBe(true);
    expect(blocked("check-config-period-value")).toBe(false);
    expect(screen.getByText("00:01:00").closest(".ph-no-capture")).toBeNull();
  });

  it("keeps an email check token out of the replay", () => {
    renderCheck("email", { token: "email-secret-token", subject: "hello" }, ["token"]);

    expect(screen.getByText("email-secret-token").closest(".ph-no-capture")).not.toBeNull();
    expect(screen.getByText("hello").closest(".ph-no-capture")).toBeNull();
  });

  it("keeps an SMTP delivery_to out of the replay, not its host", () => {
    renderCheck("smtp", { host: "mx.acme.com", delivery_to: "abc123@inbox.acme.com" }, [
      "delivery_to",
    ]);

    expect(screen.getByText("abc123@inbox.acme.com").closest(".ph-no-capture")).not.toBeNull();
    expect(blocked("check-config-delivery_to-value")).toBe(true);
    expect(screen.getByText("mx.acme.com").closest(".ph-no-capture")).toBeNull();
  });

  it("masks nothing when the response names no credential key", () => {
    renderConfig({ url: "https://acme.com", token: "not-listed" });

    expect(screen.getByTestId("check-config-view").querySelector(".ph-no-capture")).toBeNull();
  });
});
