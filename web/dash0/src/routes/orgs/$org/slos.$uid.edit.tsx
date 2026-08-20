import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import { useSlo, useUpdateSlo } from "@/api/hooks";
import { QueryErrorView } from "@/components/shared/error-views";
import { SloForm } from "@/components/slos/slo-form";
import { Skeleton } from "@/components/ui/skeleton";

export const Route = createFileRoute("/orgs/$org/slos/$uid/edit")({
  component: SloEditPage,
});

function SloEditPage() {
  const { t } = useTranslation("slos");
  const navigate = useNavigate();
  const { org, uid } = Route.useParams();

  const { data: slo, isLoading, error, refetch } = useSlo(org, uid);
  const updateSlo = useUpdateSlo(org, uid);

  if (error) {
    return <QueryErrorView error={error} org={org} onRetry={() => refetch()} />;
  }

  if (isLoading || !slo) {
    return (
      <div className="max-w-2xl space-y-6">
        <Skeleton className="h-10 w-64" />
        <Skeleton className="h-96 rounded-lg" />
      </div>
    );
  }

  return (
    <div className="max-w-2xl">
      <SloForm
        key={slo.uid}
        org={org}
        mode="edit"
        initial={slo}
        isPending={updateSlo.isPending}
        onCancel={() => navigate({ to: "/orgs/$org/slos/$uid", params: { org, uid } })}
        onSubmit={async (request) => {
          try {
            await updateSlo.mutateAsync(request);
            toast.success(t("form.updated"));
            navigate({ to: "/orgs/$org/slos/$uid", params: { org, uid } });
          } catch (err) {
            toast.error(err instanceof ApiError ? err.message : t("form.saveFailed"));
          }
        }}
      />
    </div>
  );
}
