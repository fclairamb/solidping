/**
 * @vitest-environment jsdom
 *
 * Component test for the needs-re-seal warning (spec 2026-07-16-02). Opts into
 * jsdom per-file so the default node environment (pure lib tests) is untouched.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import "@/i18n";
import { NeedsResealAlert } from "./needs-reseal-alert";

afterEach(cleanup);

describe("NeedsResealAlert", () => {
  it("renders nothing when the check needs no re-seal", () => {
    render(<NeedsResealAlert needsReseal={false} />);
    expect(screen.queryByTestId("needs-reseal-warning")).toBeNull();
  });

  it("renders nothing when the check targets no private location", () => {
    // `needsReseal` is undefined for checks with no private region at all.
    render(<NeedsResealAlert />);
    expect(screen.queryByTestId("needs-reseal-warning")).toBeNull();
  });

  it("warns, and explains the fix, when the sealed credentials are stale", () => {
    render(<NeedsResealAlert needsReseal />);

    const alert = screen.getByTestId("needs-reseal-warning");
    expect(alert).toBeTruthy();

    // Real copy, not a raw i18n key.
    expect(alert.textContent).toContain("re-sealing");
    expect(alert.textContent).not.toContain("checks:detail");

    // It must tell the operator the actual remedy: re-save the credentials.
    expect(alert.textContent).toContain("re-enter and save the credentials");
  });

  it("is a warning, never a destructive state", () => {
    render(<NeedsResealAlert needsReseal />);

    const alert = screen.getByTestId("needs-reseal-warning");
    // Destructive red is reserved for destructive actions (design reference).
    expect(alert.className).not.toContain("destructive");
  });

  it("offers the peer re-seal to an admin (spec 2026-11-10-03)", () => {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <NeedsResealAlert needsReseal org="acme" checkUid="c1" canRewrap />
      </QueryClientProvider>,
    );

    expect(screen.getByTestId("rewrap-button-check").textContent).toContain("Re-seal for the current agents");
  });

  it("does not offer it without the admin right", () => {
    render(<NeedsResealAlert needsReseal org="acme" checkUid="c1" />);

    expect(screen.queryByTestId("rewrap-button-check")).toBeNull();
  });
});
