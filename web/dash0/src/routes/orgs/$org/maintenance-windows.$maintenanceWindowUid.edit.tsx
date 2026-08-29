import { useMemo } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  useMaintenanceWindow,
  useMaintenanceWindowChecks,
  useUpdateMaintenanceWindow,
  useSetMaintenanceWindowChecks,
} from "@/api/hooks";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";
import {
  MaintenanceWindowForm,
  type MaintenanceWindowFormChecks,
} from "@/components/shared/maintenance-window-form";
import { ApiError } from "@/api/client";

export const Route = createFileRoute(
  "/orgs/$org/maintenance-windows/$maintenanceWindowUid/edit",
)({
  component: MaintenanceWindowEditPage,
});

function MaintenanceWindowEditPage() {
  const { t } = useTranslation("maintenanceWindows");
  const navigate = useNavigate();
  const { org, maintenanceWindowUid } = Route.useParams();

  const {
    data: window,
    isLoading: isWindowLoading,
    error,
    refetch,
  } = useMaintenanceWindow(org, maintenanceWindowUid);
  const { data: checks, isLoading: areChecksLoading } =
    useMaintenanceWindowChecks(org, maintenanceWindowUid);

  // The window's own fields and its check/group associations are two
  // independent queries. MaintenanceWindowForm seeds its checkUids/
  // checkGroupUids state from `initialChecks` only once, at mount (a plain
  // useState initializer, not synced on prop changes) — so the form must not
  // mount until BOTH queries have resolved. Gating on `isWindowLoading`
  // alone let the form mount with an empty selection whenever the checks
  // query was still in flight (most visible under load, e.g. right after a
  // burst of check creations), permanently dropping the persisted checks
  // from the picker until a full page reload.
  const isLoading = isWindowLoading || areChecksLoading;

  const updateWindow = useUpdateMaintenanceWindow(org, maintenanceWindowUid);
  const setChecks = useSetMaintenanceWindowChecks(org, maintenanceWindowUid);

  const initialChecks: MaintenanceWindowFormChecks = useMemo(
    () => ({
      checkUids: (checks ?? [])
        .map((c) => c.checkUid)
        .filter((u): u is string => Boolean(u)),
      checkGroupUids: (checks ?? [])
        .map((c) => c.checkGroupUid)
        .filter((u): u is string => Boolean(u)),
    }),
    [checks],
  );

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
        resource={t("title")}
        backTo="/orgs/$org/maintenance-windows"
        backLabel={t("backToList")}
        onRetry={() => refetch()}
      />
    );
  }

  if (!window) return null;

  return (
    <div className="space-y-8 max-w-2xl">
      <MaintenanceWindowForm
        mode="edit"
        initialData={window}
        initialChecks={initialChecks}
        isPending={updateWindow.isPending || setChecks.isPending}
        onCancel={() =>
          navigate({
            to: "/orgs/$org/maintenance-windows/$maintenanceWindowUid",
            params: { org, maintenanceWindowUid: window.uid },
          })
        }
        onSubmit={async ({ window: payload, checkUids, checkGroupUids }) => {
          try {
            await updateWindow.mutateAsync(payload);
            await setChecks.mutateAsync({ checkUids, checkGroupUids });
            toast.success(t("toast.updated"));
            navigate({
              to: "/orgs/$org/maintenance-windows/$maintenanceWindowUid",
              params: { org, maintenanceWindowUid: window.uid },
            });
          } catch (err) {
            toast.error(
              err instanceof ApiError ? err.message : t("toast.updateFailed"),
            );
          }
        }}
      />
    </div>
  );
}
