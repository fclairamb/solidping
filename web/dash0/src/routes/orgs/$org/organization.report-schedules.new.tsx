import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import { useCreateReportSchedule } from "@/api/hooks";
import { ReportScheduleForm } from "@/components/slos/report-schedule-form";

export const Route = createFileRoute("/orgs/$org/organization/report-schedules/new")({
  component: ReportScheduleNewPage,
});

function ReportScheduleNewPage() {
  const { t } = useTranslation("slos");
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const createSchedule = useCreateReportSchedule(org);

  return (
    <ReportScheduleForm
      org={org}
      mode="create"
      isPending={createSchedule.isPending}
      onCancel={() =>
        navigate({ to: "/orgs/$org/organization/report-schedules", params: { org } })
      }
      onSubmit={async (request) => {
        try {
          const created = await createSchedule.mutateAsync(request);
          toast.success(t("reports.form.created"));
          navigate({
            to: "/orgs/$org/organization/report-schedules/$uid",
            params: { org, uid: created.uid },
          });
        } catch (err) {
          toast.error(err instanceof ApiError ? err.message : t("reports.form.saveFailed"));
        }
      }}
    />
  );
}
