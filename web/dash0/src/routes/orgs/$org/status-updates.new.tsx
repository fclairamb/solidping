import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft } from "lucide-react";
import { toast } from "sonner";
import { useCreateStatusUpdate } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  StatusUpdateForm,
  type StatusUpdateFormData,
} from "@/components/shared/status-update-form";

export const Route = createFileRoute("/orgs/$org/status-updates/new")({
  component: NewStatusUpdatePage,
});

function NewStatusUpdatePage() {
  const { t } = useTranslation(["statusUpdates", "common"]);
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const createMutation = useCreateStatusUpdate(org);

  const handleSubmit = async (data: StatusUpdateFormData) => {
    await createMutation.mutateAsync({
      statusPageUid: data.statusPageUid,
      kind: data.kind,
      title: data.title,
      bodyMarkdown: data.bodyMarkdown,
      linkUrl: data.linkUrl || undefined,
      publishedAt: data.publishedAt
        ? new Date(data.publishedAt).toISOString()
        : undefined,
      sectionUid: data.sectionUid !== "none" ? data.sectionUid : undefined,
      checkUid: data.checkUid !== "none" ? data.checkUid : undefined,
    });
    toast.success(t("statusUpdates:toast.created"));
    navigate({ to: "/orgs/$org/status-updates", params: { org } });
  };

  return (
    <div className="space-y-6 max-w-2xl">
      <div className="flex items-start justify-between gap-4">
        <h1 className="text-3xl font-bold tracking-tight">
          {t("statusUpdates:newStatusUpdate")}
        </h1>
        <Button asChild variant="ghost" size="icon" aria-label={t("common:back")}>
          <Link to="/orgs/$org/status-updates" params={{ org }}>
            <ArrowLeft />
          </Link>
        </Button>
      </div>
      <StatusUpdateForm
        org={org}
        mode="create"
        isPending={createMutation.isPending}
        onCancel={() =>
          navigate({ to: "/orgs/$org/status-updates", params: { org } })
        }
        onSubmit={handleSubmit}
      />
    </div>
  );
}
