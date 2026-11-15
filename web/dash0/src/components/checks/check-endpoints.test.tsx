/**
 * @vitest-environment jsdom
 *
 * Spec 2026-11-15-04: dash0 records PostHog sessions unmasked, so the
 * persistent check credentials (the heartbeat URL and its commands, the email
 * check address) must render under a `ph-no-capture` element, and the checks
 * list must not carry the full address in a `title` attribute.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import "@/i18n";

const TOKEN = "hbtok_0123456789abcdef";
const EMAIL_TOKEN = "emtok0123456789abcdef";
const DOMAIN = "mail.acme.test";

vi.mock("@/api/hooks", () => ({
  useRotateHeartbeatToken: () => ({ mutateAsync: async () => ({}), isPending: false }),
  useUpdateCheck: () => ({ mutateAsync: async () => ({}), isPending: false }),
}));

vi.mock("@/api/public-config", () => ({
  useHeartbeatPush: () => ({ tcpEnabled: true, udpEnabled: true, host: "push.acme.test", tcpPort: 7001, udpPort: 7002 }),
}));

vi.mock("@/api/email-inbox", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/email-inbox")>()),
  useEmailAddressDomain: () => ({ data: DOMAIN }),
}));

import { EmailCheckTarget, EmailEndpoint, HeartbeatEndpoint } from "./check-endpoints";

afterEach(cleanup);

function blocked(element: Element): boolean {
  return element.closest(".ph-no-capture") !== null;
}

describe("check endpoint cards keep the token out of session replay", () => {
  it("heartbeat: the URL, the curl command and the push commands are blocked", () => {
    render(
      <>
        <h1 data-testid="check-name">acme-backup</h1>
        <HeartbeatEndpoint org="acme" check={{ uid: "c1", slug: "backup", config: { token: TOKEN } }} />
      </>,
    );
    const url = screen.getByTestId("heartbeat-url");
    expect(url.textContent).toContain(`/api/v1/heartbeat/acme/backup?token=${TOKEN}`);
    expect(blocked(url)).toBe(true);
    const curl = screen.getByTestId("heartbeat-curl");
    expect(curl.textContent).toContain(`curl "`);
    expect(curl.textContent).toContain(TOKEN);
    expect(blocked(curl)).toBe(true);
    // The push section is collapsed by default: open it.
    fireEvent.click(screen.getByTestId("heartbeat-push-toggle"));
    expect(blocked(screen.getByTestId("heartbeat-push-tcp"))).toBe(true);
    expect(blocked(screen.getByTestId("heartbeat-push-udp"))).toBe(true);

    // Every node whose text carries the token is blocked.
    for (const el of Array.from(document.body.querySelectorAll("*"))) {
      if (el.children.length === 0 && el.textContent?.includes(TOKEN)) expect(blocked(el)).toBe(true);
    }

    // Positive control: the check name and the card title stay recorded.
    expect(blocked(screen.getByTestId("check-name"))).toBe(false);
    expect(blocked(screen.getByTestId("heartbeat-endpoint-title"))).toBe(false);
  });

  it("heartbeat: the copy buttons still copy the full values", async () => {
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    render(<HeartbeatEndpoint org="acme" check={{ uid: "c1", slug: "backup", config: { token: TOKEN } }} />);
    fireEvent.click(screen.getByTestId("heartbeat-url-copy"));
    fireEvent.click(screen.getByTestId("heartbeat-curl-copy"));
    expect(writeText).toHaveBeenNthCalledWith(1, expect.stringContaining(`?token=${TOKEN}`));
    expect(writeText).toHaveBeenNthCalledWith(2, expect.stringMatching(/^curl ".*\?token=hbtok_/));
  });

  it("email: the address, the mailto link and the plus-addressing hint are blocked", () => {
    render(
      <>
        <h1 data-testid="check-name">acme-inbox</h1>
        <EmailEndpoint check={{ config: { token: EMAIL_TOKEN } }} />
      </>,
    );
    const address = screen.getByTestId("email-check-address");
    expect(address.textContent).toBe(`${EMAIL_TOKEN}@${DOMAIN}`);
    expect(blocked(address)).toBe(true);
    const link = screen.getByTestId("email-check-send-test");
    expect(link.getAttribute("href")).toContain(EMAIL_TOKEN);
    expect(blocked(link)).toBe(true);

    fireEvent.click(screen.getByTestId("email-check-options-toggle"));
    const hint = screen.getByTestId("email-check-help-plus");
    expect(hint.textContent).toContain(EMAIL_TOKEN);
    expect(blocked(hint)).toBe(true);

    expect(blocked(screen.getByTestId("check-name"))).toBe(false);
    expect(blocked(screen.getByTestId("email-check-options-toggle"))).toBe(false);
  });

  it("checks list: the email target is truncated and has no title with the full address", () => {
    render(<EmailCheckTarget token={EMAIL_TOKEN} domain={DOMAIN} />);
    const target = screen.getByTestId("check-row-email-target");
    expect(target.textContent).toBe(`${EMAIL_TOKEN.slice(0, 8)}…@${DOMAIN}`);
    expect(target.hasAttribute("title")).toBe(false);
    expect(target.outerHTML).not.toContain(EMAIL_TOKEN);
  });
});
