import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Star, X } from "lucide-react";
import {
  GITHUB_STAR_UI_STATE_KEY,
  onboardingUiStateKey,
  useSetUiState,
  useUiState,
  type CheckStats,
  type OnboardingUiState,
} from "@/api/hooks";
import { useGitHubStarEnabled } from "@/api/public-config";
import { useAuth } from "@/contexts/AuthContext";
import { captureEvent } from "@/lib/analytics";
import {
  checkAgeDays,
  GITHUB_REPO_URL,
  shouldShowGitHubStarPrompt,
  type GitHubStarUiState,
} from "@/lib/github-star";
import { Button, buttonVariants } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";

interface GitHubStarPromptProps {
  org: string;
  stats: CheckStats;
}

/**
 * One-time, discreet ask to star the repository, shown on the dashboard home
 * after the org has had an enabled check for a week and nothing is down. The
 * decision lives in `shouldShowGitHubStarPrompt`; the per-user state is the
 * user-global `github-star` ui-state entry. No reward, ever.
 */
export function GitHubStarPrompt({ org, stats }: GitHubStarPromptProps) {
  const enabled = useGitHubStarEnabled();
  const { user } = useAuth();
  const isDemo = Boolean(user?.isDemo);
  const active = enabled && !isDemo;

  const stateQuery = useUiState<GitHubStarUiState>(GITHUB_STAR_UI_STATE_KEY, {
    enabled: active,
  });
  const setState = useSetUiState<GitHubStarUiState>(GITHUB_STAR_UI_STATE_KEY);
  // Never alongside the getting-started checklist: it must be dismissed
  // (or have self-dismissed) before this card gets its turn.
  const onboardingQuery = useUiState<OnboardingUiState>(onboardingUiStateKey(org), {
    enabled: active,
  });

  const [hidden, setHidden] = useState(false);
  const [now] = useState(() => new Date());
  const shownWritten = useRef(false);

  const uiState = stateQuery.data;
  const visible =
    !hidden &&
    Boolean(onboardingQuery.data?.dismissedAt) &&
    shouldShowGitHubStarPrompt({
      enabled,
      isDemo,
      stats,
      uiState,
      uiStateLoaded: !stateQuery.isPending,
      now,
    });

  const needsShownWrite = visible && !uiState?.shownAt;
  useEffect(() => {
    if (!needsShownWrite || shownWritten.current) return;
    shownWritten.current = true;
    setState.mutate({ shownAt: new Date().toISOString() });
    captureEvent("github_star_prompt_shown", {
      checkAgeDays: stats.oldestEnabledCheckCreatedAt
        ? checkAgeDays(stats.oldestEnabledCheckCreatedAt, new Date())
        : 0,
    });
    // setState is a stable react-query mutation object.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [needsShownWrite]);

  if (!visible) return null;

  const shownAt = uiState?.shownAt ?? new Date().toISOString();
  const record = (outcome: "starred" | "dismissed") => {
    setHidden(true);
    setState.mutate({ shownAt, outcome });
  };

  return (
    <GitHubStarPromptCard
      onStar={() => {
        captureEvent("github_star_prompt_clicked");
        record("starred");
      }}
      onDismiss={(via) => {
        captureEvent("github_star_prompt_dismissed", { via });
        record("dismissed");
      }}
    />
  );
}

export interface GitHubStarPromptCardProps {
  onStar?: () => void;
  onDismiss?: (via: "button" | "close") => void;
}

/** Presentation only, so the design reference can render it with fixtures. */
export function GitHubStarPromptCard({ onStar, onDismiss }: GitHubStarPromptCardProps) {
  const { t } = useTranslation("dashboard");

  return (
    <Card data-testid="github-star-prompt">
      <CardContent className="flex flex-wrap items-center gap-3 py-4">
        <Star aria-hidden="true" className="h-5 w-5 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1 space-y-0.5">
          <p className="text-sm font-medium">{t("githubStar.title")}</p>
          <p className="text-sm text-muted-foreground">{t("githubStar.body")}</p>
        </div>
        <div className="flex items-center gap-2">
          <a
            href={GITHUB_REPO_URL}
            target="_blank"
            rel="noopener noreferrer"
            data-testid="github-star-prompt-star"
            className={buttonVariants({ size: "sm" })}
            onClick={onStar}
          >
            {t("githubStar.star")}
          </a>
          <Button
            variant="ghost"
            size="sm"
            data-testid="github-star-prompt-dismiss"
            onClick={() => onDismiss?.("button")}
          >
            {t("githubStar.later")}
          </Button>
          <Button
            variant="ghost"
            size="icon"
            aria-label={t("githubStar.close")}
            data-testid="github-star-prompt-close"
            onClick={() => onDismiss?.("close")}
          >
            <X className="h-4 w-4" />
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
