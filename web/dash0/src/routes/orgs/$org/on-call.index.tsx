import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { CalendarClock, Plus } from "lucide-react";

import { useOnCallSchedules } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";

export const Route = createFileRoute("/orgs/$org/on-call/")({
  component: OnCallListPage,
});

function OnCallListPage() {
  const { t } = useTranslation(["oncall", "common"]);
  const { org } = Route.useParams();
  const { data: schedules, isLoading, error, refetch } = useOnCallSchedules(org);

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

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
            <CalendarClock className="h-7 w-7 text-muted-foreground" />
            {t("oncall:list.title")}
          </h1>
          <p className="text-muted-foreground">{t("oncall:list.subtitle")}</p>
        </div>
        <Button asChild>
          <Link to="/orgs/$org/on-call/new" params={{ org }}>
            <Plus className="h-4 w-4 mr-1" />
            {t("oncall:list.create")}
          </Link>
        </Button>
      </div>

      {isLoading ? (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {[...Array(3)].map((_, i) => (
            <Skeleton key={i} className="h-40 rounded" />
          ))}
        </div>
      ) : !schedules || schedules.length === 0 ? (
        <Card>
          <CardContent className="py-10 text-center text-sm text-muted-foreground">
            {t("oncall:list.empty")}
          </CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {schedules.map((schedule) => (
            <Card key={schedule.uid} className="hover:shadow-md transition-shadow">
              <CardHeader>
                <CardTitle>
                  <Link
                    to="/orgs/$org/on-call/$slug"
                    params={{ org, slug: schedule.slug }}
                    className="hover:underline"
                  >
                    {schedule.name}
                  </Link>
                </CardTitle>
                <CardDescription>
                  {schedule.timezone} · {t(`oncall:detail.rotation${schedule.rotationType === "daily" ? "Daily" : "Weekly"}`)}
                </CardDescription>
              </CardHeader>
              <CardContent className="space-y-2 text-sm">
                <div>
                  <span className="text-muted-foreground">{t("oncall:list.currentlyOnCall")}: </span>
                  {schedule.currentlyOnCall ? (
                    <span className="font-medium">{schedule.currentlyOnCall.name}</span>
                  ) : (
                    <span className="text-muted-foreground">{t("oncall:list.noOneOnCall")}</span>
                  )}
                </div>
              </CardContent>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
