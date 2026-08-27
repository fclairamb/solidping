import { RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useServerVersionStatus } from "@/hooks/use-server-version-status";

/** `"dev"` (untagged local builds) renders as the literal word, never `"vdev"`. */
function formatVersion(version: string): string {
  return version === "dev" ? version : `v${version}`;
}

/**
 * Discreet server-version indicator, mounted in the sidebar footer utility
 * row alongside LanguageSwitcher/ThemeToggle/LiveStatusDot (spec
 * 2026-08-28-01). This app's `<Sidebar>` never opts into the shadcn
 * "icon rail" collapse mode (no `collapsible="icon"` prop anywhere in
 * AppSidebar/$org.tsx — the default is `collapsible="offcanvas"`, which
 * hides the whole sidebar rather than shrinking it), so there is no
 * partial-width state to special-case here; the mobile Sheet renders these
 * same footer contents at its own (wider) width. Passive in the common
 * case: the current version renders small and muted.
 *
 * When the server has been redeployed since this page loaded
 * (`useServerVersionStatus().isStale`), a red reload icon appears — the one
 * deliberate exception to "destructive red is for destructive actions" in
 * this codebase, kept on the icon only, per the spec.
 */
export function ServerVersionIndicator() {
  const { t } = useTranslation();
  const { loadedVersion, currentVersion, isStale } = useServerVersionStatus();

  if (!currentVersion) {
    return null;
  }

  if (!isStale) {
    return (
      <span
        className="text-xs text-muted-foreground"
        data-testid="server-version-text"
      >
        {formatVersion(currentVersion)}
      </span>
    );
  }

  return (
    <div className="flex items-center gap-1" data-testid="server-version-indicator">
      <span
        className="text-xs text-muted-foreground"
        data-testid="server-version-text"
      >
        {formatVersion(loadedVersion ?? currentVersion)}
      </span>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            onClick={() => window.location.reload()}
            className="inline-flex shrink-0 items-center justify-center rounded-md text-destructive hover:opacity-80 h-6 w-6"
            aria-label={t("serverVersion.reloadAria")}
            data-testid="server-version-reload"
          >
            <RefreshCw className="h-3.5 w-3.5" />
          </button>
        </TooltipTrigger>
        <TooltipContent>
          {t("serverVersion.stale", { version: formatVersion(currentVersion) })}
        </TooltipContent>
      </Tooltip>
    </div>
  );
}
