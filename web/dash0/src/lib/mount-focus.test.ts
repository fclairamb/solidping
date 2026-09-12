import { describe, expect, it, vi } from "vitest";

import { shouldFocusOnMount } from "./mount-focus";

// Both branches, because the e2e layer can only reach one of them: Playwright's
// setViewportSize changes the viewport but not the pointer media feature, so
// headless Chromium reports `(pointer: fine)` even at 375px.
describe("shouldFocusOnMount", () => {
  it("focuses on a fine pointer (mouse or trackpad)", () => {
    const matchMedia = vi.fn(() => ({ matches: true }));
    expect(shouldFocusOnMount(matchMedia)).toBe(true);
    expect(matchMedia).toHaveBeenCalledWith("(pointer: fine)");
  });

  it("does NOT focus on a coarse pointer (touch), where it would raise the keyboard", () => {
    const matchMedia = vi.fn((query: string) => ({
      // What a phone answers: the fine-pointer query does not match.
      matches: query === "(pointer: coarse)",
    }));
    expect(shouldFocusOnMount(matchMedia)).toBe(false);
  });

  it("does not focus when there is no matchMedia to ask", () => {
    expect(shouldFocusOnMount(undefined)).toBe(false);
    expect(shouldFocusOnMount(null)).toBe(false);
  });

  it("does not focus when matchMedia throws", () => {
    expect(
      shouldFocusOnMount(() => {
        throw new Error("nope");
      }),
    ).toBe(false);
  });
});
