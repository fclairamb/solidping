import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import { useCreateMaintenanceWindow } from "@/api/hooks";
import { MaintenanceWindowForm } from "@/components/shared/maintenance-window-form";
import { apiFetch, ApiError } from "@/api/client";

export const Route = createFileRoute("/orgs/$org/maintenance-windows/new")({
  component: MaintenanceWindowNewPage,
});

function MaintenanceWindowNewPage() {
  const { t } = useTranslation("maintenanceWindows");
  const navigate = useNavigate();
  const { org } = Route.useParams();
  const queryClient = useQueryClient();
  const createWindow = useCreateMaintenanceWindow(org);
  // The checks PUT is bound to the *new* window's UID, which only exists after
  // the create call resolves. useSetMaintenanceWindowChecks bakes the UID into
  // its mutationFn at hook-construction time, so the second step is issued with
  // a direct apiFetch using the freshly created UID instead.
  const [submitting, setSubmitting] = useState(false);

  return (
    <MaintenanceWindowForm
      mode="create"
      isPending={createWindow.isPending || submitting}
      onCancel={() =>
        navigate({ to: "/orgs/$org/maintenance-windows", params: { org } })
      }
      onSubmit={async ({ window, checkUids, checkGroupUids }) => {
        setSubmitting(true);
        try {
          const created = await createWindow.mutateAsync(window);
          // Two-step create: associate checks with a follow-up PUT now that we
          // have the new UID.
          if (checkUids.length || checkGroupUids.length) {
            await apiFetch<void>(
              `/api/v1/orgs/${org}/maintenance-windows/${created.uid}/checks`,
              {
                method: "PUT",
                body: JSON.stringify({ checkUids, checkGroupUids }),
              },
            );
            queryClient.invalidateQueries({
              queryKey: ["maintenanceWindowChecks", org, created.uid],
            });
          }
          toast.success(t("toast.created"));
          navigate({
            to: "/orgs/$org/maintenance-windows/$maintenanceWindowUid",
            params: { org, maintenanceWindowUid: created.uid },
          });
        } catch (err) {
          toast.error(
            err instanceof ApiError ? err.message : t("toast.createFailed"),
          );
        } finally {
          setSubmitting(false);
        }
      }}
    />
  );
}
