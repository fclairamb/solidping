/**
 * @vitest-environment jsdom
 *
 * Component tests for the org over-limit banner (spec 2026-08-26-03). Opts into
 * jsdom per-file so the default node environment (pure lib tests) is untouched.
 */
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import { CheckRateLimitBanner } from "./check-rate-limit-banner";

// The "view usage" action is a @tanstack/react-router <Link>, which needs a
// <RouterProvider> in scope. Routing isn't what's under test — stub it to a
// plain anchor so the banner renders standalone.
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: PropsWithChildren<Record<string, unknown>>) => (
    <a {...props}>{children}</a>
  ),
}));

afterEach(cleanup);

const ORG = "acmetech";

describe("CheckRateLimitBanner", () => {
  it("renders nothing when the server sent no figure", () => {
    render(<CheckRateLimitBanner org={ORG} />);
    expect(screen.queryByTestId("check-rate-limit-banner")).toBeNull();
  });

  it("renders nothing for an org inside its cap that lost nothing", () => {
    render(
      <CheckRateLimitBanner
        org={ORG}
        checksPerMinute={{ demand: 4, limit: 10, skippedToday: 0 }}
      />,
    );
    expect(screen.queryByTestId("check-rate-limit-banner")).toBeNull();
  });

  it("renders nothing for an unlimited org, however much it schedules", () => {
    render(
      <CheckRateLimitBanner
        org={ORG}
        checksPerMinute={{ demand: 5000, limit: null, skippedToday: 0 }}
      />,
    );
    expect(screen.queryByTestId("check-rate-limit-banner")).toBeNull();
  });

  it("states the demand and the cap when the org is over its limit", () => {
    render(
      <CheckRateLimitBanner
        org={ORG}
        checksPerMinute={{ demand: 240, limit: 120, skippedToday: 0 }}
      />,
    );

    const banner = screen.getByTestId("check-rate-limit-banner");
    expect(banner).toBeTruthy();

    // Real copy with the numbers interpolated, not a raw i18n key.
    expect(banner.textContent).toContain("240");
    expect(banner.textContent).toContain("120");
    expect(banner.textContent).not.toContain("org:checkRateLimit");

    // Predictive only: nothing has been lost yet, so don't claim it has.
    expect(screen.queryByTestId("check-rate-limit-skipped")).toBeNull();
  });

  it("reports skipped executions even when demand is back under the cap", () => {
    render(
      <CheckRateLimitBanner
        org={ORG}
        checksPerMinute={{ demand: 4, limit: 10, skippedToday: 613 }}
      />,
    );

    const banner = screen.getByTestId("check-rate-limit-banner");
    expect(banner).toBeTruthy();
    expect(screen.getByTestId("check-rate-limit-skipped").textContent).toContain(
      "613",
    );
  });

  it("shows both halves when the org is over its cap AND lost executions", () => {
    render(
      <CheckRateLimitBanner
        org={ORG}
        checksPerMinute={{ demand: 240, limit: 120, skippedToday: 613 }}
      />,
    );

    const banner = screen.getByTestId("check-rate-limit-banner");
    expect(banner.textContent).toContain("240");
    expect(screen.getByTestId("check-rate-limit-skipped")).toBeTruthy();
  });

  it("offers the usage link only where it was asked for", () => {
    const { rerender } = render(
      <CheckRateLimitBanner
        org={ORG}
        checksPerMinute={{ demand: 240, limit: 120, skippedToday: 0 }}
        showUsageLink
      />,
    );
    expect(screen.getByTestId("check-rate-limit-usage-link")).toBeTruthy();

    // On the Usage page itself the link would point at the current page.
    rerender(
      <CheckRateLimitBanner
        org={ORG}
        checksPerMinute={{ demand: 240, limit: 120, skippedToday: 0 }}
      />,
    );
    expect(screen.queryByTestId("check-rate-limit-usage-link")).toBeNull();
  });

  it("offers the upgrade CTA only when the deployment configures one", () => {
    const { rerender } = render(
      <CheckRateLimitBanner
        org={ORG}
        checksPerMinute={{ demand: 240, limit: 120, skippedToday: 0 }}
      />,
    );
    expect(screen.queryByTestId("check-rate-limit-upgrade-link")).toBeNull();

    rerender(
      <CheckRateLimitBanner
        org={ORG}
        checksPerMinute={{ demand: 240, limit: 120, skippedToday: 0 }}
        upgradeUrl="https://billing.example.com/upgrade#bt=token"
      />,
    );
    const cta = screen.getByTestId("check-rate-limit-upgrade-link");
    expect(cta.getAttribute("href")).toBe(
      "https://billing.example.com/upgrade#bt=token",
    );
  });

  it("is a warning, never a destructive state", () => {
    render(
      <CheckRateLimitBanner
        org={ORG}
        checksPerMinute={{ demand: 240, limit: 120, skippedToday: 613 }}
      />,
    );

    // Destructive red is reserved for destructive actions (design reference):
    // being over a plan limit is a warning, not a deletion.
    expect(
      screen.getByTestId("check-rate-limit-banner").className,
    ).not.toContain("destructive");
  });
});
