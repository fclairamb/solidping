import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { usePublicStatusPage } from "@/api/hooks";
import { StatusPageView } from "@/components/shared/status-page-view";
import { useLanguageFromPage } from "@/hooks/useLanguageFromPage";
import { readSpPage } from "@/lib/sp-page";

export const Route = createFileRoute("/")({
  component: IndexPage,
});

function IndexPage() {
  const spPage = readSpPage();

  if (spPage) {
    return <CustomDomainStatusPage org={spPage.org} slug={spPage.slug} />;
  }

  return <DefaultLanding />;
}

// CustomDomainStatusPage renders the host-resolved status page. It mirrors the
// $org/$slug route's view exactly, but keeps the address bar on the custom host.
function CustomDomainStatusPage({ org, slug }: { org: string; slug: string }) {
  const { t } = useTranslation();
  const { data: page, isLoading, error } = usePublicStatusPage(org, slug);

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

  return <StatusPageView page={page} org={org} />;
}

function DefaultLanding() {
  const { t } = useTranslation();

  return (
    <div className="min-h-screen flex items-center justify-center">
      <div className="text-center">
        <h1 className="text-2xl font-bold">{t("solidpingStatus")}</h1>
        <p className="mt-2 text-muted-foreground">{t("visitStatusPage")}</p>
      </div>
    </div>
  );
}
