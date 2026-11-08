/**
 * @vitest-environment jsdom
 *
 * Spec 2026-11-08-02: the prompt's analytics events and its "never alongside
 * the onboarding checklist" rule.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

const captureEvent = vi.fn();
const mutate = vi.fn();
let uiState: { shownAt?: string; outcome?: string } | null = null;

vi.mock("@/lib/analytics", () => ({
  captureEvent: (...args: unknown[]) => captureEvent(...args),
}));
vi.mock("@/api/public-config", () => ({ useGitHubStarEnabled: () => true }));
vi.mock("@/contexts/AuthContext", () => ({ useAuth: () => ({ user: { isDemo: false } }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (k: string) => k }) }));
vi.mock("@/api/hooks", () => ({
  GITHUB_STAR_UI_STATE_KEY: "github-star",
  useUiState: () => ({ data: uiState, isPending: false }),
  useSetUiState: () => ({ mutate }),
}));

import { GitHubStarPrompt } from "./github-star-prompt";

const DAY_MS = 24 * 60 * 60 * 1000;
const stats = {
  down: 0,
  oldestEnabledCheckCreatedAt: new Date(Date.now() - 9 * DAY_MS - 1000).toISOString(),
} as never;

describe("GitHubStarPrompt", () => {
  beforeEach(() => {
    uiState = null;
    captureEvent.mockClear();
    mutate.mockClear();
  });
  afterEach(cleanup);

  it("stays hidden while the onboarding checklist is visible", () => {
    render(<GitHubStarPrompt stats={stats} onboardingVisible />);
    expect(screen.queryByTestId("github-star-prompt")).toBeNull();
    expect(captureEvent).not.toHaveBeenCalled();
  });

  it("captures shown with checkAgeDays", () => {
    render(<GitHubStarPrompt stats={stats} onboardingVisible={false} />);
    expect(screen.getByTestId("github-star-prompt")).toBeTruthy();
    expect(captureEvent).toHaveBeenCalledWith("github_star_prompt_shown", { checkAgeDays: 9 });
    expect(mutate).toHaveBeenCalledWith(
      expect.objectContaining({ shownAt: expect.any(String) }),
    );
  });

  it("captures clicked on star", () => {
    render(<GitHubStarPrompt stats={stats} onboardingVisible={false} />);
    fireEvent.click(screen.getByTestId("github-star-prompt-star"));
    expect(captureEvent).toHaveBeenCalledWith("github_star_prompt_clicked");
    expect(mutate).toHaveBeenLastCalledWith(expect.objectContaining({ outcome: "starred" }));
  });

  it.each([
    ["github-star-prompt-dismiss", "button"],
    ["github-star-prompt-close", "close"],
  ])("captures dismissed via %s", (testId, via) => {
    render(<GitHubStarPrompt stats={stats} onboardingVisible={false} />);
    fireEvent.click(screen.getByTestId(testId));
    expect(captureEvent).toHaveBeenCalledWith("github_star_prompt_dismissed", { via });
    expect(mutate).toHaveBeenLastCalledWith(expect.objectContaining({ outcome: "dismissed" }));
  });
});
