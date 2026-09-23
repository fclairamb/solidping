/**
 * @vitest-environment jsdom
 *
 * Component tests for the degraded dry-run banner (spec 2026-09-22-03). This
 * banner IS the rollout: degraded detection ships off for every pre-existing
 * check, and without a visible "it would have fired at 14:37 — enable?" prompt
 * the whole feature is a column nobody turns on.
 */
import type { PropsWithChildren } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import type { Check } from "@/api/hooks";
import { DegradedDryRunBanner } from "./degraded-dry-run-banner";

// The "see the window" action is a @tanstack/react-router <Link>, which needs a
// <RouterProvider> in scope. Routing isn't what's under test — stub it to a
// plain anchor, as check-rate-limit-banner.test.tsx does.
vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    ...props
  }: PropsWithChildren<Record<string, unknown>>) => (
    <a {...props}>{children}</a>
  ),
}));

// useUpdateCheck reaches react-query; the enable action is exercised through
// this stub rather than through a QueryClientProvider, because what matters here
// is WHEN the banner renders, not how the PATCH travels.
const mutateAsync = vi.fn().mockResolvedValue({});

vi.mock("@/api/hooks", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/hooks")>();

  return { ...actual, useUpdateCheck: () => ({ mutateAsync }) };
});

afterEach(cleanup);

const ORG = "acmetech";

function check(overrides: Partial<Check>): Check {
  return {
    uid: "11111111-1111-1111-1111-111111111111",
    name: "acme",
    type: "http",
    ...overrides,
  } as Check;
}

describe("DegradedDryRunBanner", () => {
  it("renders nothing when the dry run has never fired", () => {
    render(
      <DegradedDryRunBanner
        org={ORG}
        check={check({ degradedEnabled: false })}
      />,
    );
    expect(screen.queryByTestId("degraded-dry-run-banner")).toBeNull();
  });

  it("renders nothing once the check is enabled", () => {
    // The evaluator clears the stamp on its next sweep; this guard covers the
    // seconds in between, where both fields look true.
    render(
      <DegradedDryRunBanner
        org={ORG}
        check={check({
          degradedEnabled: true,
          degradedWouldFireAt: "2026-09-22T14:37:00Z",
        })}
      />,
    );
    expect(screen.queryByTestId("degraded-dry-run-banner")).toBeNull();
  });

  it("says when the rules would have fired, and offers to enable them", () => {
    render(
      <DegradedDryRunBanner
        org={ORG}
        check={check({
          degradedEnabled: false,
          degradedWouldFireAt: "2026-09-22T14:37:00Z",
        })}
        windowUrl={{ graphFrom: 1_790_000_000_000, graphTo: 1_790_003_600_000 }}
      />,
    );

    const banner = screen.getByTestId("degraded-dry-run-banner");
    expect(banner).toBeTruthy();

    // Real copy, not a raw i18n key, and the timestamp is interpolated.
    expect(banner.textContent).toContain("degraded");
    expect(banner.textContent).not.toContain("checks:detail");
    expect(banner.textContent).toContain("2026");

    expect(screen.getByTestId("degraded-enable-button")).toBeTruthy();
    expect(screen.getByTestId("degraded-dry-run-window-link")).toBeTruthy();
  });

  it("omits the window link when there is no window to link to", () => {
    render(
      <DegradedDryRunBanner
        org={ORG}
        check={check({
          degradedEnabled: false,
          degradedWouldFireAt: "2026-09-22T14:37:00Z",
        })}
      />,
    );

    expect(screen.getByTestId("degraded-dry-run-banner")).toBeTruthy();
    expect(screen.queryByTestId("degraded-dry-run-window-link")).toBeNull();
  });

  it("is a warning, never a destructive state", () => {
    render(
      <DegradedDryRunBanner
        org={ORG}
        check={check({
          degradedEnabled: false,
          degradedWouldFireAt: "2026-09-22T14:37:00Z",
        })}
      />,
    );

    // The check is not down — destructive red is reserved for destructive
    // actions (design reference), and amber is the whole point of this feature.
    expect(
      screen.getByTestId("degraded-dry-run-banner").className,
    ).not.toContain("destructive");
  });
});
