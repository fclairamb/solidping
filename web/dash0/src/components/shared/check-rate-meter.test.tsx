/**
 * @vitest-environment jsdom
 *
 * Component tests for the scheduling page's quota meter (spec 2026-08-26-04).
 * Opts into jsdom per-file so the default node environment (pure lib tests) is
 * untouched.
 */
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import "@/i18n";
import { CheckRateMeter } from "./check-rate-meter";

afterEach(cleanup);

describe("CheckRateMeter", () => {
  it("shows only the saved figure when nothing is pending", () => {
    render(<CheckRateMeter saved={84} draft={84} limit={120} />);

    expect(
      screen.getByTestId("check-rate-meter-total").textContent,
    ).toContain("84");
    expect(screen.queryByTestId("check-rate-meter-saved")).toBeNull();
    expect(
      screen.getByTestId("check-rate-meter").getAttribute("data-over"),
    ).toBe("false");
  });

  it("shows the now → after pair while a draft is pending", () => {
    // The point of the meter: the consequence of an unsaved edit must be
    // legible BEFORE anything is written.
    render(<CheckRateMeter saved={240} draft={96} limit={120} />);

    expect(
      screen.getByTestId("check-rate-meter-saved").textContent,
    ).toContain("240");
    expect(
      screen.getByTestId("check-rate-meter-total").textContent,
    ).toContain("96");
  });

  it("flags over-cap on the DRAFT, not on what is saved", () => {
    // Saved is over, draft is under: the user has already fixed it, and the
    // meter must say so rather than keep shouting about the old state.
    render(<CheckRateMeter saved={240} draft={96} limit={120} />);
    expect(
      screen.getByTestId("check-rate-meter").getAttribute("data-over"),
    ).toBe("false");

    cleanup();

    // Saved is under, draft went over: the warning must appear immediately.
    render(<CheckRateMeter saved={96} draft={240} limit={120} />);
    expect(
      screen.getByTestId("check-rate-meter").getAttribute("data-over"),
    ).toBe("true");
  });

  it("is not over at exactly the cap", () => {
    render(<CheckRateMeter saved={120} draft={120} limit={120} />);
    expect(
      screen.getByTestId("check-rate-meter").getAttribute("data-over"),
    ).toBe("false");
  });

  it("treats an absent limit as unlimited: no bar, never over", () => {
    render(<CheckRateMeter saved={5000} draft={5000} limit={null} />);

    expect(
      screen.getByTestId("check-rate-meter").getAttribute("data-over"),
    ).toBe("false");
    expect(screen.queryByRole("progressbar")).toBeNull();
  });

  it("recognises a zero cap as a real cap, not as unlimited", () => {
    // 0 is falsy in JS — an org suspended to 0/min must read as over, not as
    // uncapped.
    render(<CheckRateMeter saved={1} draft={1} limit={0} />);

    expect(
      screen.getByTestId("check-rate-meter").getAttribute("data-over"),
    ).toBe("true");
  });

  it("formats a fractional rate to one decimal", () => {
    render(<CheckRateMeter saved={0.2} draft={0.2} limit={10} />);
    expect(
      screen.getByTestId("check-rate-meter-total").textContent,
    ).toContain("0.2");
  });
});
