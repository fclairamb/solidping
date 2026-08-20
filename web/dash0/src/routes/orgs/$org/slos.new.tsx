import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import { useCreateSlo } from "@/api/hooks";
import { SloForm } from "@/components/slos/slo-form";

export const Route = createFileRoute("/orgs/$org/slos/new")({
  component: SloNewPage,
});

function SloNewPage() {
  const { t } = useTranslation("slos");
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const createSlo = useCreateSlo(org);

  return (
    <SloForm
      org={org}
      mode="create"
      isPending={createSlo.isPending}
      onCancel={() => navigate({ to: "/orgs/$org/slos", params: { org } })}
      onSubmit={async (request) => {
        try {
          const created = await createSlo.mutateAsync(request);
          toast.success(t("form.created"));
          navigate({ to: "/orgs/$org/slos/$uid", params: { org, uid: created.uid } });
        } catch (err) {
          toast.error(err instanceof ApiError ? err.message : t("form.saveFailed"));
        }
      }}
    />
  );
}
