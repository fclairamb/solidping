import { useTranslation } from "react-i18next";
import { useStagedPublicStatusPage } from "@/api/hooks";
import { StatusPageView } from "@/components/shared/status-page-view";
import { useLanguageFromPage } from "@/hooks/useLanguageFromPage";
import { readSpPage } from "@/lib/sp-page";

/**
 * The "/" route: resolve whatever page this host addresses.
 *
 * On the installation's own host that is the plain landing (visit-a-status-page
 * hint); on a custom domain the server stamped the target page into the SPA
 * shell and this renders it without touching the address bar.
 *
 * Lives outside routes/index.tsx so that file exports only `Route`
 * (react-refresh/only-export-components).
 */
export function IndexPage() {
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
  const { page, isLoading, error, ...stages } =
    useStagedPublicStatusPage(org, slug);

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

  return <StatusPageView page={page} org={org} stages={stages} />;
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
