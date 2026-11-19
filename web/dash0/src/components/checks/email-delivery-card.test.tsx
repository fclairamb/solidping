/**
 * @vitest-environment jsdom
 *
 * Spec 2026-11-18-03: a mail without SolidPing's sent-at header gets its
 * latency from its Date header; the card shows it, flagged as the sender's
 * clock, and has no link to a sending SMTP check.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: { children: React.ReactNode }) => (
    <a data-testid={(props as Record<string, string>)["data-testid"]}>{children}</a>
  ),
}));

const { EmailDeliveryCard } = await import("./email-delivery-card");

afterEach(cleanup);

describe("EmailDeliveryCard send-time source", () => {
  it("shows the Date-header source next to the latency, without the SMTP link", () => {
    render(
      <EmailDeliveryCard
        org="acme"
        output={{ sentAt: "2026-11-18T10:00:00Z", sentAtSource: "date", latencyMs: 809000 }}
      />,
    );
    expect(screen.getByTestId("email-delivery-latency").textContent).toBe("809.0 s");
    expect(screen.getByTestId("email-delivery-sent-at-source")).toBeTruthy();
    expect(screen.queryByTestId("email-delivery-source-link")).toBeNull();
  });

  it("does not flag the SolidPing header source", () => {
    render(
      <EmailDeliveryCard
        org="acme"
        output={{
          sourceCheckUid: "smtp-1",
          sentAt: "2026-11-18T10:00:00Z",
          sentAtSource: "header",
          latencyMs: 1500,
        }}
      />,
    );
    expect(screen.getByTestId("email-delivery-latency")).toBeTruthy();
    expect(screen.queryByTestId("email-delivery-sent-at-source")).toBeNull();
    expect(screen.getByTestId("email-delivery-source-link")).toBeTruthy();
  });

  it("renders nothing without a send time or attribution", () => {
    render(<EmailDeliveryCard org="acme" output={{ message: "Email received" }} />);
    expect(screen.queryByTestId("email-delivery-card")).toBeNull();
  });
});
