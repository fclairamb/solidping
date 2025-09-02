import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import { Calendar, RefreshCw, User, Cpu } from "lucide-react";
import { useEvents } from "@/api/hooks";
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

export const Route = createFileRoute("/orgs/$org/events")({
  component: EventsPage,
});

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

function getEventIcon(eventType?: string) {
  if (!eventType) return <Calendar className="h-4 w-4" />;

  if (eventType.startsWith("check.")) {
    return <Cpu className="h-4 w-4 text-blue-400" />;
  }
  if (eventType.startsWith("incident.")) {
    return <Calendar className="h-4 w-4 text-yellow-500" />;
  }
  return <Calendar className="h-4 w-4" />;
}

function getEventLabel(eventType: string | undefined, t: (key: string, options?: Record<string, unknown>) => string): string {
  if (!eventType) return t("unknown");
  return t(`types.${eventType}`, { defaultValue: eventType });
}

function EventsPage() {
  const { t } = useTranslation("events");
  const { org } = Route.useParams();
  const [typeFilter, setTypeFilter] = useState<EventType | "all">("all");

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
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{t("title")}</h1>
          <p className="text-muted-foreground">
            {t("subtitle")}
          </p>
        </div>
      </div>

      <div className="flex items-center gap-4">
        <Select
          value={typeFilter}
          onValueChange={(v) => setTypeFilter(v as EventType | "all")}
        >
          <SelectTrigger className="w-[200px]">
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
          size="icon"
          onClick={() => refetch()}
          disabled={isRefetching}
        >
          <RefreshCw
            className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`}
          />
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
        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("table.time")}</TableHead>
                <TableHead>{t("table.event")}</TableHead>
                <TableHead>{t("table.actor")}</TableHead>
                <TableHead>{t("table.related")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {events.data.map((event) => (
                <TableRow key={event.uid}>
                  <TableCell className="text-sm">
                    {event.createdAt
                      ? new Date(event.createdAt).toLocaleString()
                      : "-"}
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-2">
                      {getEventIcon(event.eventType)}
                      <Badge variant="outline" className="text-xs">
                        {getEventLabel(event.eventType, t)}
                      </Badge>
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1 text-sm">
                      {event.actorType === "user" ? (
                        <User className="h-3 w-3 text-muted-foreground" />
                      ) : (
                        <Cpu className="h-3 w-3 text-muted-foreground" />
                      )}
                      <span className="capitalize">
                        {t(`actorTypes.${event.actorType || "system"}`)}
                      </span>
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex gap-2">
                      {event.checkUid && (
                        <Link
                          to="/orgs/$org/checks/$checkUid"
                          params={{ org, checkUid: event.checkUid }}
                          search={{ graphPeriod: undefined, graphFull: undefined }}
                          className="text-xs text-primary hover:underline"
                        >
                          {t("links.check")}
                        </Link>
                      )}
                      {event.incidentUid && (
                        <Link
                          to="/orgs/$org/incidents/$incidentUid"
                          params={{ org, incidentUid: event.incidentUid }}
                          className="text-xs text-primary hover:underline"
                        >
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
      ) : (
        <div className="text-center py-12 text-muted-foreground">
          <Calendar className="h-12 w-12 mx-auto mb-4 opacity-50" />
          <p className="text-lg font-medium">{t("noEvents")}</p>
          <p className="text-sm">
            {typeFilter !== "all"
              ? t("noEventsMatchFilter")
              : t("noEventsRecorded")}
          </p>
        </div>
      )}
    </div>
  );
}
