import { useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft } from "lucide-react";

import { useCreateOnCallSchedule } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  OnCallScheduleForm,
  type OnCallScheduleFormValues,
} from "@/components/oncall/on-call-schedule-form";

export const Route = createFileRoute("/orgs/$org/on-call/new")({
  component: NewOnCallSchedulePage,
});

function NewOnCallSchedulePage() {
  const { t } = useTranslation(["oncall", "common"]);
  const { org } = Route.useParams();
  const navigate = useNavigate();

  const create = useCreateOnCallSchedule(org);
  const [error, setError] = useState<string | null>(null);

  const handleSubmit = async (values: OnCallScheduleFormValues) => {
    setError(null);
    try {
      const created = await create.mutateAsync({
        slug: values.slug,
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
      navigate({
        to: "/orgs/$org/on-call/$slug",
        params: { org, slug: created.slug },
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : t("oncall:errors.generic"));
    }
  };

  return (
    <div className="space-y-6 max-w-2xl">
      <div className="flex items-start justify-between gap-4">
        <h1 className="text-3xl font-bold tracking-tight">
          {t("oncall:form.create")}
        </h1>
        <Button
          asChild
          variant="ghost"
          size="icon"
          aria-label={t("oncall:detail.back")}
        >
          <Link to="/orgs/$org/on-call" params={{ org }}>
            <ArrowLeft />
          </Link>
        </Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("oncall:form.create")}</CardTitle>
          <CardDescription>{t("oncall:detail.v1Notes")}</CardDescription>
        </CardHeader>
        <CardContent>
          <OnCallScheduleForm
            mode="create"
            org={org}
            onSubmit={handleSubmit}
            onCancel={() =>
              navigate({ to: "/orgs/$org/on-call", params: { org } })
            }
            submitting={create.isPending}
            error={error}
          />
        </CardContent>
      </Card>
    </div>
  );
}
