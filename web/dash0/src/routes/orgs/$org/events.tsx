import { useTranslation } from "react-i18next";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Calendar, RefreshCw } from "lucide-react";
import { useEvents } from "@/api/hooks";
import { EventLogTable } from "@/components/dashboard/event-log-table";
import { getEventLabel } from "@/components/dashboard/event-display";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { QueryErrorView } from "@/components/shared/error-views";
import { PageHeader } from "@/components/shared/page-header";
import {
  EVENT_TYPE_FAMILIES,
  eventTypesOfFamily,
  isEventType,
  type EventType,
} from "@/lib/event-types";

interface EventsSearch {
  type?: EventType;
}

export const Route = createFileRoute("/orgs/$org/events")({
  component: EventsPage,
  // The type filter is this page's core navigation — it decides what the page
  // is showing — so it lives in the URL rather than in useState: bookmarkable,
  // deep-linkable, and it survives a refresh. Anything unrecognized normalizes
  // back to "all" (undefined) instead of filtering to nothing. The catalogue
  // is lib/event-types.ts, which event-types.test.ts pins to the server's own
  // EventType* constants — so the filter offers every event type, not a
  // hand-picked subset.
  validateSearch: (search: Record<string, unknown>): EventsSearch =>
    isEventType(search.type) ? { type: search.type } : {},
});

function EventsPage() {
  const { t } = useTranslation("events");
  const { org } = Route.useParams();
  const { type } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  // The catalogue is re-checked here rather than trusting validateSearch
  // alone: the router keeps search params the validator did not return, so a
  // hand-edited `?type=anything` still reaches this component, and an unknown
  // value leaves the Select with no matching item — an empty trigger over an
  // empty table, filtered to a type that cannot exist.
  const typeFilter: EventType | "all" = isEventType(type) ? type : "all";

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
          <SelectTrigger
            className="w-full sm:w-[260px]"
            data-testid="events-type-filter"
          >
            <SelectValue placeholder={t("filterByType")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("allEvents")}</SelectItem>
            {EVENT_TYPE_FAMILIES.map((family) => {
              const types = eventTypesOfFamily(family);
              if (types.length === 0) return null;

              return (
                <SelectGroup key={family}>
                  <SelectLabel>{t(`audit.families.${family}`)}</SelectLabel>
                  {types.map((value) => (
                    <SelectItem key={value} value={value}>
                      {getEventLabel(value, t)}
                    </SelectItem>
                  ))}
                </SelectGroup>
              );
            })}
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
