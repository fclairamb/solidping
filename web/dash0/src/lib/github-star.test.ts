import { describe, expect, it } from "vitest";
import { shouldShowGitHubStarPrompt, type GitHubStarPromptInput } from "./github-star";

const now = new Date("2026-11-08T12:00:00Z");
const ago = (ms: number) => new Date(now.getTime() - ms).toISOString();
const H = 3600 * 1000;
const D = 24 * H;

function input(over: Partial<GitHubStarPromptInput> = {}): GitHubStarPromptInput {
  return {
    enabled: true,
    isDemo: false,
    stats: { oldestEnabledCheckCreatedAt: ago(8 * D), down: 0 },
    uiState: null,
    uiStateLoaded: true,
    now,
    ...over,
  };
}

describe("shouldShowGitHubStarPrompt", () => {
  it("shows for an eligible org", () => {
    expect(shouldShowGitHubStarPrompt(input())).toBe(true);
  });

  it("requires the check to be at least 7 days old", () => {
    const at = (age: number) => input({ stats: { oldestEnabledCheckCreatedAt: ago(age), down: 0 } });
    expect(shouldShowGitHubStarPrompt(at(7 * D))).toBe(true);
    expect(shouldShowGitHubStarPrompt(at(7 * D - H))).toBe(false);
  });

  it("is false without an enabled check", () => {
    expect(
      shouldShowGitHubStarPrompt(input({ stats: { oldestEnabledCheckCreatedAt: null, down: 0 } })),
    ).toBe(false);
  });

  it("is false while something is down", () => {
    expect(
      shouldShowGitHubStarPrompt(input({ stats: { oldestEnabledCheckCreatedAt: ago(8 * D), down: 1 } })),
    ).toBe(false);
  });

  it("is false in a demo session and when disabled", () => {
    expect(shouldShowGitHubStarPrompt(input({ isDemo: true }))).toBe(false);
    expect(shouldShowGitHubStarPrompt(input({ enabled: false }))).toBe(false);
  });

  it("is false until the ui-state is loaded", () => {
    expect(shouldShowGitHubStarPrompt(input({ uiStateLoaded: false }))).toBe(false);
  });

  it("is false once an outcome is recorded", () => {
    for (const outcome of ["starred", "dismissed"] as const) {
      expect(shouldShowGitHubStarPrompt(input({ uiState: { outcome } }))).toBe(false);
    }
  });

  it("stops 14 days after the first display", () => {
    expect(shouldShowGitHubStarPrompt(input({ uiState: { shownAt: ago(13 * D) } }))).toBe(true);
    expect(shouldShowGitHubStarPrompt(input({ uiState: { shownAt: ago(15 * D) } }))).toBe(false);
  });
});
