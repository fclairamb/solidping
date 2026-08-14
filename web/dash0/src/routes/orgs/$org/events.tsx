import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { AlertTriangle, Calendar, RefreshCw, User, Cpu } from "lucide-react";
import { useEvents } from "@/api/hooks";
import {
  getEventCheckName,
  getEventLabel,
  getEventTone,
} from "@/components/dashboard/event-display";
import { DurationAgo } from "@/components/shared/relative-time";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
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
        <div className="overflow-hidden rounded-xl border bg-card shadow-card">
          <div className="overflow-x-auto">
            <Table>
              <TableHeader className="bg-muted/30">
                <TableRow>
                  <TableHead className="w-[140px]">{t("table.time")}</TableHead>
                  <TableHead className="w-[220px]">{t("table.event")}</TableHead>
                  <TableHead className="w-[120px]">{t("table.actor")}</TableHead>
                  <TableHead>{t("table.related")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {events.data.map((event) => (
                  <TableRow
                    key={event.uid}
                    className="transition-colors hover:bg-muted/40"
                  >
                    <TableCell className="whitespace-nowrap font-mono text-xs text-muted-foreground">
                      {event.createdAt ? (
                        // Relative reads faster when scanning a log; the exact
                        // timestamp stays one hover away.
                        <span title={new Date(event.createdAt).toLocaleString()}>
                          <DurationAgo since={event.createdAt} />
                        </span>
                      ) : (
                        "—"
                      )}
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant="outline"
                        className={cn(
                          "gap-1.5 text-xs font-medium",
                          getEventTone(event.eventType),
                        )}
                      >
                        {getEventLabel(event.eventType, t)}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
                        {event.actorType === "user" ? (
                          <User className="h-3 w-3 shrink-0 text-muted-foreground/70" />
                        ) : (
                          <Cpu className="h-3 w-3 shrink-0 text-muted-foreground/70" />
                        )}
                        <span className="capitalize">
                          {t(`actorTypes.${event.actorType || "system"}`)}
                        </span>
                      </span>
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-wrap items-center gap-2">
                        {event.checkUid && (
                          <Link
                            to="/orgs/$org/checks/$checkUid"
                            params={{ org, checkUid: event.checkUid }}
                            search={{
                              graphPeriod: undefined,
                              graphFull: undefined,
                              region: undefined,
                            }}
                            className="inline-flex items-center gap-1.5 text-xs font-medium text-foreground transition-colors hover:text-primary hover:underline"
                          >
                            <Cpu className="h-3 w-3 shrink-0 text-muted-foreground/70" />
                            {getEventCheckName(event) ?? t("links.check")}
                          </Link>
                        )}
                        {event.incidentUid && (
                          <Link
                            to="/orgs/$org/incidents/$incidentUid"
                            params={{ org, incidentUid: event.incidentUid }}
                            className="inline-flex items-center gap-1 rounded border bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground transition-colors hover:text-foreground"
                          >
                            <AlertTriangle className="h-3 w-3 shrink-0" />
                            {t("links.incident")}
                          </Link>
                        )}
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </div>
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
