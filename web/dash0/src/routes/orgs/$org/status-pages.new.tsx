import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { useCreateStatusPage } from "@/api/hooks";
import { StatusPageForm } from "@/components/shared/status-page-form";

export const Route = createFileRoute("/orgs/$org/status-pages/new")({
  component: StatusPageNewPage,
});

function StatusPageNewPage() {
  const { t } = useTranslation("statusPages");
  const navigate = useNavigate();
  const { org } = Route.useParams();
  const createStatusPage = useCreateStatusPage(org);

  return (
    <StatusPageForm
      mode="create"
      isPending={createStatusPage.isPending}
      onCancel={() => navigate({ to: "/orgs/$org/status-pages", params: { org } })}
      onSubmit={async (data) => {
        const page = await createStatusPage.mutateAsync({
          name: data.name,
          slug: data.slug,
          description: data.description || undefined,
          visibility: data.visibility,
          isDefault: data.isDefault || undefined,
        });
        toast.success(t("toast.created"));
        navigate({
          to: "/orgs/$org/status-pages/$statusPageUid",
          params: { org, statusPageUid: page.uid },
        });
      }}
    />
  );
}
