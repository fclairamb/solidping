/**
 * @vitest-environment jsdom
 *
 * Component tests for the shared live-demo banner (spec 2026-09-06-02).
 *
 * The banner is the ONLY place a visitor is told the two facts that make the
 * demo honest — everything is shared, and everything they create disappears —
 * so "it renders, with the real copy, and cannot be dismissed" is a product
 * requirement rather than a styling detail.
 */
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import { DemoBanner } from "./demo-banner";

// Routing isn't under test: the sign-up CTA is a @tanstack/react-router <Link>,
// which needs a <RouterProvider> in scope. Stub it to a plain anchor that keeps
// its `to` prop visible so the destination stays assertable.
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: PropsWithChildren<Record<string, unknown>>) => (
    <a {...props}>{children}</a>
  ),
}));

const mockUser = vi.hoisted(() => ({ current: null as { isDemo: boolean } | null }));

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ user: mockUser.current }),
}));

afterEach(() => {
  cleanup();
  mockUser.current = null;
});

const ORG = "acmetech";

describe("DemoBanner", () => {
  it("renders nothing when there is no session at all", () => {
    render(<DemoBanner org={ORG} />);
    expect(screen.queryByTestId("demo-banner")).toBeNull();
  });

  it("renders nothing for an ordinary session", () => {
    // This is what keeps the component inert on every self-hosted install that
    // has no demo configured — it ships in the bundle either way.
    mockUser.current = { isDemo: false };
    render(<DemoBanner org={ORG} />);
    expect(screen.queryByTestId("demo-banner")).toBeNull();
  });

  it("states both facts, in real copy, for a demo session", () => {
    mockUser.current = { isDemo: true };
    render(<DemoBanner org={ORG} />);

    const banner = screen.getByTestId("demo-banner");
    expect(banner).toBeTruthy();

    // Real copy with the locale resolved, not a raw i18n key.
    expect(banner.textContent).not.toContain("org:demo");
    // Shared with other visitors...
    expect(banner.textContent).toContain("visitors");
    // ...and temporary. Both halves, or the banner is misleading.
    expect(banner.textContent).toContain("hour");
  });

  it("offers the in-app sign-up route as the conversion hook", () => {
    mockUser.current = { isDemo: true };
    render(<DemoBanner org={ORG} />);

    const cta = screen.getByTestId("demo-banner-signup");
    expect(cta.getAttribute("to")).toBe("/orgs/$org/register");
  });

  it("cannot be dismissed", () => {
    // Deliberate (see the component's own note): a visitor who closes this in
    // the first ten seconds then spends twenty minutes not knowing their work
    // is temporary. No close control may ever appear here.
    mockUser.current = { isDemo: true };
    render(<DemoBanner org={ORG} />);

    const banner = screen.getByTestId("demo-banner");
    const buttons = Array.from(banner.querySelectorAll("button"));
    expect(
      buttons.some((b) =>
        /dismiss|close/i.test(
          `${b.textContent ?? ""} ${b.getAttribute("aria-label") ?? ""}`,
        ),
      ),
    ).toBe(false);
  });

  it("is informational, never a destructive state", () => {
    // The shared, temporary nature of the demo is the deal, not a malfunction.
    mockUser.current = { isDemo: true };
    render(<DemoBanner org={ORG} />);
    expect(screen.getByTestId("demo-banner").className).not.toContain(
      "destructive",
    );
  });
});
