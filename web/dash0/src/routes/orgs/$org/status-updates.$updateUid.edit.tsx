import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft } from "lucide-react";
import { toast } from "sonner";
import { useStatusUpdate, useUpdateStatusUpdate } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";
import {
  StatusUpdateForm,
  type StatusUpdateFormData,
} from "@/components/shared/status-update-form";

export const Route = createFileRoute(
  "/orgs/$org/status-updates/$updateUid/edit"
)({
  component: EditStatusUpdatePage,
});

function EditStatusUpdatePage() {
  const { t } = useTranslation(["statusUpdates", "common"]);
  const { org, updateUid } = Route.useParams();
  const navigate = useNavigate();

  const { data: update, isLoading, error, refetch } = useStatusUpdate(org, updateUid);
  const updateMutation = useUpdateStatusUpdate(org, updateUid);

  const handleSubmit = async (data: StatusUpdateFormData) => {
    // sectionUid/checkUid/linkUrl are presence-aware nullable fields on the
    // API: sending `null` clears them, `undefined` (an omitted key) would
    // leave them untouched instead — always send all three explicitly so
    // "No section" / "No check" / an emptied link actually persists.
    // incidentUid has no field in this form, so it is never sent (untouched).
    await updateMutation.mutateAsync({
      kind: data.kind,
      title: data.title,
      bodyMarkdown: data.bodyMarkdown,
      linkUrl: data.linkUrl || null,
      publishedAt: new Date(data.publishedAt).toISOString(),
      sectionUid: data.sectionUid !== "none" ? data.sectionUid : null,
      checkUid: data.checkUid !== "none" ? data.checkUid : null,
    });
    toast.success(t("statusUpdates:toast.updated"));
    navigate({ to: "/orgs/$org/status-updates", params: { org } });
  };

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource="status update"
        onRetry={() => refetch()}
      />
    );
  }

  if (isLoading || !update) {
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

  return (
    <div className="space-y-6 max-w-2xl">
      <div className="flex items-start justify-between gap-4">
        <h1 className="text-3xl font-bold tracking-tight">
          {t("statusUpdates:editStatusUpdate")}
        </h1>
        <Button asChild variant="ghost" size="icon" aria-label={t("common:back")}>
          <Link to="/orgs/$org/status-updates" params={{ org }}>
            <ArrowLeft />
          </Link>
        </Button>
      </div>
      <StatusUpdateForm
        org={org}
        mode="edit"
        initialData={update}
        isPending={updateMutation.isPending}
        onCancel={() =>
          navigate({ to: "/orgs/$org/status-updates", params: { org } })
        }
        onSubmit={handleSubmit}
      />
    </div>
  );
}
