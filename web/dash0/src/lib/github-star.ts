import type { CheckStats } from "@/api/hooks";

export const GITHUB_REPO_URL = "https://github.com/fclairamb/solidping";

const DAY_MS = 24 * 60 * 60 * 1000;
/** The org must have had an enabled check for this long. */
export const MIN_CHECK_AGE_DAYS = 7;
/** An ignored prompt stops showing this long after its first display. */
export const PROMPT_LIFETIME_DAYS = 14;

/** Value stored under the `github-star` ui-state key. */
export interface GitHubStarUiState {
  shownAt?: string;
  outcome?: "starred" | "dismissed";
}

export interface GitHubStarPromptInput {
  /** Public config `githubStar.enabled`; a missing key means on. */
  enabled: boolean;
  isDemo: boolean;
  stats: Pick<CheckStats, "oldestEnabledCheckCreatedAt" | "down">;
  uiState: GitHubStarUiState | null | undefined;
  uiStateLoaded: boolean;
  now: Date;
}

/** Whether the one-time "star us on GitHub" prompt may be shown. */
export function shouldShowGitHubStarPrompt(input: GitHubStarPromptInput): boolean {
  const { enabled, isDemo, stats, uiState, uiStateLoaded, now } = input;
  if (!enabled || isDemo || !uiStateLoaded) return false;
  if (uiState?.outcome) return false;
  if (stats.down !== 0) return false;

  const oldest = stats.oldestEnabledCheckCreatedAt;
  if (!oldest) return false;
  const oldestMs = Date.parse(oldest);
  if (Number.isNaN(oldestMs) || now.getTime() - oldestMs < MIN_CHECK_AGE_DAYS * DAY_MS) {
    return false;
  }

  if (uiState?.shownAt) {
    const shownMs = Date.parse(uiState.shownAt);
    if (!Number.isNaN(shownMs) && now.getTime() - shownMs >= PROMPT_LIFETIME_DAYS * DAY_MS) {
      return false;
    }
  }

  return true;
}

/** Whole days between the oldest enabled check and now. */
export function checkAgeDays(oldest: string, now: Date): number {
  return Math.floor((now.getTime() - Date.parse(oldest)) / DAY_MS);
}
