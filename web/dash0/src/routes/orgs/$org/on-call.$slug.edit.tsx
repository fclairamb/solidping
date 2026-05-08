import { useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft } from "lucide-react";
import { toast } from "sonner";

import { useOnCallSchedule, useUpdateOnCallSchedule } from "@/api/hooks";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { QueryErrorView } from "@/components/shared/error-views";
import {
  OnCallScheduleForm,
  type OnCallScheduleFormValues,
} from "@/components/oncall/on-call-schedule-form";

export const Route = createFileRoute("/orgs/$org/on-call/$slug/edit")({
  component: EditOnCallSchedulePage,
});

function EditOnCallSchedulePage() {
  const { t } = useTranslation(["oncall", "common"]);
  const { org, slug } = Route.useParams();
  const navigate = useNavigate();

  const { data: schedule, isLoading, error, refetch } = useOnCallSchedule(org, slug);
  const update = useUpdateOnCallSchedule(org, slug);
  const [submitError, setSubmitError] = useState<string | null>(null);

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("oncall:list.title")}
        onRetry={() => refetch()}
      />
    );
  }

  if (isLoading || !schedule) {
    return (
      <div className="p-6 text-muted-foreground">{t("oncall:detail.previewLoading")}</div>
    );
  }

  const initialValues: Partial<OnCallScheduleFormValues> = {
    name: schedule.name,
    slug: schedule.slug,
    description: schedule.description ?? "",
    timezone: schedule.timezone,
    rotationType: schedule.rotationType,
    handoffTime: schedule.handoffTime,
    handoffWeekday: schedule.handoffWeekday ?? 0,
    startAt: schedule.startAt,
    userUids: schedule.userUids ?? [],
  };

  const handleSubmit = async (values: OnCallScheduleFormValues) => {
    setSubmitError(null);
    try {
      await update.mutateAsync({
        name: values.name,
        description: values.description || undefined,
        timezone: values.timezone,
        rotationType: values.rotationType,
        handoffTime: values.handoffTime,
        handoffWeekday:
          values.rotationType === "weekly" ? values.handoffWeekday : undefined,
        startAt: values.startAt,
        userUids: values.userUids,
      });
      toast.success(t("oncall:toast.updated"));
      navigate({
        to: "/orgs/$org/on-call/$slug",
        params: { org, slug },
      });
    } catch (err) {
      setSubmitError(
        err instanceof Error ? err.message : t("oncall:toast.updateFailed"),
      );
    }
  };

  return (
    <div className="space-y-6 max-w-2xl">
      <div>
        <Link
          to="/orgs/$org/on-call/$slug"
          params={{ org, slug }}
          className="text-sm text-muted-foreground hover:underline flex items-center gap-1 mb-2"
        >
          <ArrowLeft className="h-3 w-3" />
          {t("oncall:detail.back")}
        </Link>
        <h1 className="text-3xl font-bold tracking-tight">
          {t("oncall:form.edit")}
        </h1>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("oncall:form.edit")}</CardTitle>
          <CardDescription>{schedule.name}</CardDescription>
        </CardHeader>
        <CardContent>
          <OnCallScheduleForm
            mode="edit"
            org={org}
            initialValues={initialValues}
            onSubmit={handleSubmit}
            onCancel={() =>
              navigate({
                to: "/orgs/$org/on-call/$slug",
                params: { org, slug },
              })
            }
            submitting={update.isPending}
            error={submitError}
          />
        </CardContent>
      </Card>
    </div>
  );
}
