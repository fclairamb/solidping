import { createFileRoute } from "@tanstack/react-router";
import { useDefaultStatusPage } from "@/api/hooks";
import { StatusPageView } from "@/components/shared/status-page-view";
import { useTranslation } from "react-i18next";
import { useLanguageFromPage } from "@/hooks/useLanguageFromPage";

export const Route = createFileRoute("/$org")({
  component: DefaultStatusPage,
});

function DefaultStatusPage() {
  const { t } = useTranslation();
  const { org } = Route.useParams();
  const { data: page, isLoading, error } = useDefaultStatusPage(org);

  useLanguageFromPage(page?.language);

  if (isLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center">
        <div className="text-muted-foreground">{t("loading")}</div>
      </div>
    );
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

  return <StatusPageView page={page} />;
}
