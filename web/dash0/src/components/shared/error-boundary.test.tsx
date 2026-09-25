/**
 * @vitest-environment jsdom
 *
 * Spec 2026-09-25-13: the root ErrorBoundary must report a crash to PostHog
 * error tracking (with the component stack) exactly once — not zero (the
 * original bug: exception autocapture was off and console.error alone never
 * reached PostHog) and not twice (capture_console_errors autocapture of the
 * same console.error call, de-duplicated separately in analytics.test.ts's
 * dedupeAutocapturedBoundaryExceptions tests).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import { ErrorBoundary } from "./error-boundary";

vi.mock("@/lib/analytics", () => ({
  captureException: vi.fn(),
}));

import { captureException } from "@/lib/analytics";

function Boom(): never {
  throw new Error("kaboom");
}

describe("ErrorBoundary", () => {
  beforeEach(() => {
    vi.mocked(captureException).mockClear();
    // componentDidCatch also logs to console.error for the bug-report ring
    // buffer (kept deliberately, see the module header) — silence it so the
    // test output stays clean; it is not what this test verifies.
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("reports the crash to PostHog error tracking exactly once, with the component stack", () => {
    render(
      <ErrorBoundary>
        <Boom />
      </ErrorBoundary>,
    );

    expect(captureException).toHaveBeenCalledTimes(1);
    const [error, properties] = vi.mocked(captureException).mock.calls[0]!;
    expect((error as Error).message).toBe("kaboom");
    expect(properties).toMatchObject({ componentStack: expect.stringContaining("Boom") });

    // The fallback card renders instead of a blank page.
    expect(screen.getByText(/something/i)).toBeTruthy();
  });

  it("does not report anything when children render fine", () => {
    render(
      <ErrorBoundary>
        <div>all good</div>
      </ErrorBoundary>,
    );

    expect(captureException).not.toHaveBeenCalled();
    expect(screen.getByText("all good")).toBeTruthy();
  });
});
