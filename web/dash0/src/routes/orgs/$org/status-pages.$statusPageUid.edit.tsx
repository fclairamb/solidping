import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Palette } from "lucide-react";
import { toast } from "sonner";
import { useStatusPage, useUpdateStatusPage } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { QueryErrorView } from "@/components/shared/error-views";
import { StatusPageForm } from "@/components/shared/status-page-form";
import { StatusPageBranding } from "@/components/shared/status-page-branding";
import { StatusPageCustomDomain } from "@/components/shared/status-page-custom-domain";
import { StatusPageSubscribers } from "@/components/shared/status-page-subscribers";

export const Route = createFileRoute(
  "/orgs/$org/status-pages/$statusPageUid/edit"
)({
  component: StatusPageEditPage,
});

function StatusPageEditPage() {
  const { t } = useTranslation("statusPages");
  const navigate = useNavigate();
  const { org, statusPageUid } = Route.useParams();
  const { data: page, isLoading, error, refetch } = useStatusPage(org, statusPageUid);
  const updateStatusPage = useUpdateStatusPage(org, statusPageUid);

  if (isLoading) {
    return (
      <div className="space-y-6 max-w-2xl">
        <div className="flex items-center gap-4">
          <Skeleton className="h-10 w-10 rounded" />
          <Skeleton className="h-8 w-48" />
        </div>
        <Skeleton className="h-96 rounded-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource="Status Page"
        backTo="/orgs/$org/status-pages"
        backLabel="Back to Status Pages"
        onRetry={() => refetch()}
      />
    );
  }

  if (!page) return null;

  return (
    <div className="space-y-8 max-w-2xl">
      <StatusPageForm
        mode="edit"
        initialData={page}
        isPending={updateStatusPage.isPending}
        onCancel={() =>
          navigate({
            to: "/orgs/$org/status-pages/$statusPageUid",
            params: { org, statusPageUid: page.uid },
          })
        }
        onSubmit={async (data) => {
          await updateStatusPage.mutateAsync({
            name: data.name,
            slug: data.slug,
            description: data.description || undefined,
            visibility: data.visibility,
            isDefault: data.isDefault,
            enabled: data.enabled,
            showAvailability: data.showAvailability,
            showResponseTime: data.showResponseTime,
            historyPeriod: data.historyPeriod,
            hideBranding: data.hideBranding,
            password: data.password,
            autoPublish: data.autoPublish,
            autoPublishDelaySeconds: data.autoPublishDelaySeconds,
            autoResolve: data.autoResolve,
            settings: data.settings,
          });
          toast.success(t("toast.updated"));
          navigate({
            to: "/orgs/$org/status-pages/$statusPageUid",
            params: { org, statusPageUid: page.uid },
          });
        }}
      />

      {/* Appearance lives on its own route rather than in this form: it needs
          the full width for the side-by-side live preview iframe. */}
      <Card data-testid="status-page-appearance-card">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Palette className="h-4 w-4" />
            {t("appearance.cardTitle")}
          </CardTitle>
          <CardDescription>{t("appearance.cardDescription")}</CardDescription>
        </CardHeader>
        <CardContent>
          <Link
            to="/orgs/$org/status-pages/$statusPageUid/appearance"
            params={{ org, statusPageUid: page.uid }}
          >
            <Button variant="outline" data-testid="status-page-appearance-link">
              <Palette className="mr-2 h-4 w-4" />
              {t("appearance.cardCta")}
            </Button>
          </Link>
        </CardContent>
      </Card>

      <StatusPageBranding org={org} page={page} />

      <StatusPageCustomDomain org={org} page={page} />

      <StatusPageSubscribers org={org} statusPageUid={page.uid} />
    </div>
  );
}
