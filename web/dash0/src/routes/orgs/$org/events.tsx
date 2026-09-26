import { useTranslation } from "react-i18next";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Calendar, RefreshCw } from "lucide-react";
import { useEvents } from "@/api/hooks";
import { EventLogTable } from "@/components/dashboard/event-log-table";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { QueryErrorView } from "@/components/shared/error-views";
import { PageHeader } from "@/components/shared/page-header";

type EventType =
  | "check.created"
  | "check.updated"
  | "check.deleted"
  | "incident.created"
  | "incident.acknowledged"
  | "incident.escalated"
  | "incident.resolved";

const eventTypeValues: (EventType | "all")[] = [
  "all",
  "check.created",
  "check.updated",
  "check.deleted",
  "incident.created",
  "incident.acknowledged",
  "incident.escalated",
  "incident.resolved",
];

interface EventsSearch {
  type?: EventType;
}

export const Route = createFileRoute("/orgs/$org/events")({
  component: EventsPage,
  // The type filter is this page's core navigation — it decides what the page
  // is showing — so it lives in the URL rather than in useState: bookmarkable,
  // deep-linkable, and it survives a refresh. Anything unrecognized normalizes
  // back to "all" (undefined) instead of filtering to nothing.
  validateSearch: (search: Record<string, unknown>): EventsSearch => {
    const type = search.type;
    return typeof type === "string" &&
      (eventTypeValues as string[]).includes(type) &&
      type !== "all"
      ? { type: type as EventType }
      : {};
  },
});

function EventsPage() {
  const { t } = useTranslation("events");
  const { org } = Route.useParams();
  const { type } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const typeFilter: EventType | "all" = type ?? "all";

  const {
    data: events,
    isLoading,
    error,
    refetch,
    isRefetching,
  } = useEvents(org, {
    eventType: typeFilter === "all" ? undefined : typeFilter,
    size: 50,
  });

  return (
    <div className="space-y-6">
      <PageHeader
        icon={Calendar}
        title={t("title")}
        description={t("subtitle")}
        docsHref="/docs/features/events"
        className="flex-wrap"
      />

      <div className="flex flex-wrap items-center justify-between gap-4">
        <Select
          value={typeFilter}
          onValueChange={(v) =>
            navigate({
              to: ".",
              search: v === "all" ? {} : { type: v as EventType },
              replace: true,
            })
          }
        >
          <SelectTrigger className="w-[200px]" data-testid="events-type-filter">
            <SelectValue placeholder={t("filterByType")} />
          </SelectTrigger>
          <SelectContent>
            {eventTypeValues.map((value) => (
              <SelectItem key={value} value={value}>
                {value === "all" ? t("allEvents") : t(`types.${value}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button
          variant="outline"
          onClick={() => refetch()}
          disabled={isRefetching}
          aria-label={t("common:refresh")}
        >
          <RefreshCw
            className={`h-4 w-4 sm:mr-2 ${isRefetching ? "animate-spin" : ""}`}
          />
          <span className="hidden sm:inline">{t("common:refresh")}</span>
        </Button>
      </div>

      {error ? (
        <QueryErrorView error={error} org={org} onRetry={() => refetch()} />
      ) : isLoading ? (
        <div className="space-y-3">
          {[...Array(10)].map((_, i) => (
            <Skeleton key={i} className="h-14 rounded-lg" />
          ))}
        </div>
      ) : events?.data && events.data.length > 0 ? (
        <EventLogTable org={org} events={events.data} t={t} />
      ) : (
        <div className="space-y-3 rounded-xl border bg-card p-12 text-center shadow-card">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
            <Calendar className="h-6 w-6 text-muted-foreground" />
          </div>
          <p className="text-sm font-medium text-foreground">{t("noEvents")}</p>
          <p className="mx-auto max-w-sm text-xs text-muted-foreground">
            {typeFilter !== "all"
              ? t("noEventsMatchFilter")
              : t("noEventsRecorded")}
          </p>
        </div>
      )}
    </div>
  );
}
