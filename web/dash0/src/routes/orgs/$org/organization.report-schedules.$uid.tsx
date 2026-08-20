import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import { useReportSchedule, useUpdateReportSchedule } from "@/api/hooks";
import { QueryErrorView } from "@/components/shared/error-views";
import { ReportScheduleForm } from "@/components/slos/report-schedule-form";
import { Skeleton } from "@/components/ui/skeleton";

export const Route = createFileRoute("/orgs/$org/organization/report-schedules/$uid")({
  component: ReportScheduleEditPage,
});

function ReportScheduleEditPage() {
  const { t } = useTranslation("slos");
  const { org, uid } = Route.useParams();
  const navigate = useNavigate();
  const { data: schedule, isLoading, error, refetch } = useReportSchedule(org, uid);
  const updateSchedule = useUpdateReportSchedule(org, uid);

  if (error) {
    return <QueryErrorView error={error} onRetry={() => refetch()} />;
  }

  if (isLoading || !schedule) {
    return <Skeleton className="h-64 w-full" />;
  }

  return (
    <ReportScheduleForm
      key={schedule.uid}
      org={org}
      mode="edit"
      initial={schedule}
      isPending={updateSchedule.isPending}
      onCancel={() =>
        navigate({ to: "/orgs/$org/organization/report-schedules", params: { org } })
      }
      onSubmit={async (request) => {
        try {
          await updateSchedule.mutateAsync(request);
          toast.success(t("reports.form.updated"));
        } catch (err) {
          toast.error(err instanceof ApiError ? err.message : t("reports.form.saveFailed"));
        }
      }}
    />
  );
}
