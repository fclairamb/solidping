import { useParams } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { isLockedError, useStagedPublicStatusPage } from "@/api/hooks";
import { StatusPageView } from "@/components/shared/status-page-view";
import { UnlockForm } from "@/components/shared/unlock-form";
import { useLanguageFromPage } from "@/hooks/useLanguageFromPage";

/**
 * The org's default status page (no slug in the URL).
 *
 * Lives outside routes/$org.index.tsx so that file exports only `Route`
 * (react-refresh/only-export-components).
 */
export function DefaultStatusPage() {
  const { t } = useTranslation();
  const { org } = useParams({ from: "/$org/" });
  const { page, isLoading, error, refetch, ...stages } =
    useStagedPublicStatusPage(org, null);

  useLanguageFromPage(page?.language);

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="text-muted-foreground">{t("loading")}</div>
      </div>
    );
  }

  // Same as the /$org/$slug route: a locked page is not a missing page. The
  // default page has no slug in the URL, so the unlock posts without one.
  if (isLockedError(error)) {
    return <UnlockForm org={org} onUnlocked={() => refetch()} />;
  }

  if (error || !page) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="text-center">
          <h1 className="text-2xl font-bold">{t("statusPageNotFound")}</h1>
          <p className="mt-2 text-muted-foreground">
            {t("statusPageNotFoundDescription")}
          </p>
        </div>
      </div>
    );
  }

  return <StatusPageView page={page} org={org} stages={stages} />;
}
