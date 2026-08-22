import { useEffect } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { isLockedError, usePublicStatusPage } from "@/api/hooks";
import { StatusPageView } from "@/components/shared/status-page-view";
import { UnlockForm } from "@/components/shared/unlock-form";
import { useTranslation } from "react-i18next";
import { useLanguageFromPage } from "@/hooks/useLanguageFromPage";

export const Route = createFileRoute("/$org/$slug")({
  component: StatusPageRoute,
});

function StatusPageRoute() {
  const { t } = useTranslation();
  const { org, slug } = Route.useParams();
  const {
    data: page,
    isLoading,
    error,
    refetch,
  } = usePublicStatusPage(org, slug);

  useLanguageFromPage(page?.language);

  // Deep-link support: dash0 links here as #update-{uid}. The updates render
  // after the page loads, so a native hash jump on first paint can miss — once
  // the page is ready, scroll to the matching card if it's within the history
  // window. If the update is out of range (no element), this is a no-op and the
  // page stays at the top.
  useEffect(() => {
    if (!page) return;
    const hash = window.location.hash;
    if (!hash.startsWith("#update-")) return;
    const el = document.getElementById(hash.slice(1));
    if (el) {
      el.scrollIntoView({ behavior: "smooth", block: "start" });
    }
  }, [page]);

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="text-muted-foreground">{t("loading")}</div>
      </div>
    );
  }

  // A password-protected page answers 401 STATUS_PAGE_LOCKED, which is NOT a
  // "not found": the page exists and the visitor can get in. Checked before
  // the generic error branch so a locked page never renders as missing.
  if (isLockedError(error)) {
    return <UnlockForm org={org} slug={slug} onUnlocked={() => refetch()} />;
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

  return <StatusPageView page={page} org={org} />;
}
