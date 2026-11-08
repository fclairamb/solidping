import { Star } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useGitHubStarEnabled } from "@/api/public-config";
import { SidebarMenuButton, SidebarMenuItem } from "@/components/ui/sidebar";
import { captureEvent } from "@/lib/analytics";
import { GITHUB_REPO_URL } from "@/lib/github-star";

/**
 * Permanent, discreet "Star on GitHub" row in the sidebar footer. No star
 * count and no request to api.github.com: it is a plain link. Renders nothing
 * when the operator turned it off (SP_APP_STARPROMPT=false).
 */
export function GitHubStarLink() {
  const { t } = useTranslation("common");
  const enabled = useGitHubStarEnabled();

  if (!enabled) return null;

  return (
    <SidebarMenuItem>
      <SidebarMenuButton
        asChild
        size="sm"
        className="text-muted-foreground"
        tooltip={t("githubStar.sidebar")}
      >
        <a
          href={GITHUB_REPO_URL}
          target="_blank"
          rel="noopener noreferrer"
          data-testid="github-star-sidebar"
          onClick={() => captureEvent("github_star_link_clicked", { source: "sidebar" })}
        >
          <Star />
          <span>{t("githubStar.sidebar")}</span>
        </a>
      </SidebarMenuButton>
    </SidebarMenuItem>
  );
}
