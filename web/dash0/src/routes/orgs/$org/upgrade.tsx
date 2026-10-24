import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute } from "@tanstack/react-router";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Skeleton } from "@/components/ui/skeleton";
import { useEntitlements } from "@/api/hooks";
import { upgradeView } from "@/lib/upgrade-redirect";

export const Route = createFileRoute("/orgs/$org/upgrade")({
  component: UpgradePage,
});

// Landing page for emails (spec 2026-10-22-01): an email never links to the
// billing service, so it points here and the signed billing token is minted
// by GET /entitlements at click time, for the logged-in viewer only.
function UpgradePage() {
  const { t } = useTranslation("org");
  const { org } = Route.useParams();
  const { data, isLoading, isError } = useEntitlements(org);
  const view = upgradeView({ isLoading, isError, upgradeUrl: data?.upgradeUrl });

  useEffect(() => {
    if (view.kind === "redirect") window.location.replace(view.url);
  }, [view]);

  return (
    <div className="mx-auto max-w-xl p-4" data-testid="upgrade-page">
      {view.kind === "unavailable" ? (
        <Alert data-testid="upgrade-unavailable">
          <AlertTitle>{t("upgradePage.title")}</AlertTitle>
          <AlertDescription>{t("upgradePage.unavailable")}</AlertDescription>
        </Alert>
      ) : (
        <div className="space-y-2" data-testid="upgrade-redirecting">
          <p className="text-sm text-muted-foreground">{t("upgradePage.redirecting")}</p>
          <Skeleton className="h-4 w-40" />
        </div>
      )}
    </div>
  );
}
