import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { AlertTriangle, CheckCircle, RefreshCw } from "lucide-react";
import { useIncidents, type IncidentDetail } from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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

type StateFilter = "all" | "active" | "resolved";

export const Route = createFileRoute("/orgs/$org/incidents/")({
  validateSearch: (search: Record<string, unknown>) => ({
    state: (["all", "active", "resolved"].includes(search.state as string)
      ? search.state
      : "all") as StateFilter,
  }),
  component: IncidentsIndexPage,
});

function formatDuration(ms: number): string {
  const seconds = Math.floor(ms / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);

  if (days > 0) return `${days}d ${hours % 24}h`;
  if (hours > 0) return `${hours}h ${minutes % 60}m`;
  if (minutes > 0) return `${minutes}m`;
  return `${seconds}s`;
}

function IncidentDuration({ incident }: { incident: IncidentDetail }) {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (incident.state === "active" && !incident.resolvedAt) {
      const interval = setInterval(() => setNow(Date.now()), 1000);
      return () => clearInterval(interval);
    }
  }, [incident.state, incident.resolvedAt]);

  if (incident.startedAt && incident.resolvedAt) {
    return formatDuration(
      new Date(incident.resolvedAt).getTime() -
        new Date(incident.startedAt).getTime()
    );
  }
  if (incident.startedAt) {
    return formatDuration(now - new Date(incident.startedAt).getTime());
  }
  return "-";
}

function formatRelativeTime(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffSec = Math.floor(diffMs / 1000);
  const diffMin = Math.floor(diffSec / 60);
  const diffHour = Math.floor(diffMin / 60);
  const diffDay = Math.floor(diffHour / 24);

  if (diffDay > 30) return date.toLocaleDateString();
  if (diffDay > 0) return `${diffDay}d ago`;
  if (diffHour > 0) return `${diffHour}h ago`;
  if (diffMin > 0) return `${diffMin}m ago`;
  return "just now";
}

function IncidentsIndexPage() {
  const { t } = useTranslation("incidents");
  const { org } = Route.useParams();
  const { state: stateFilter } = Route.useSearch();
  const navigate = useNavigate();

  const {
    data: incidents,
    isLoading,
    error,
    refetch,
    isRefetching,
  } = useIncidents(org, {
    state: stateFilter === "all" ? undefined : stateFilter,
    size: 50,
    with: "check",
  });

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight flex items-center gap-2">
            <AlertTriangle className="h-7 w-7 text-muted-foreground" />
            {t("title")}
          </h1>
          <p className="text-muted-foreground">
            {t("subtitle")}
          </p>
        </div>
      </div>

      <div className="flex items-center gap-4">
        <Select
          value={stateFilter}
          onValueChange={(v) =>
            navigate({
              to: ".",
              search: { state: v as StateFilter },
              replace: true,
            })
          }
        >
          <SelectTrigger className="w-[180px]" data-testid="incidents-state-filter">
            <SelectValue placeholder={t("filterByState")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("allIncidents")}</SelectItem>
            <SelectItem value="active">{t("activeOnly")}</SelectItem>
            <SelectItem value="resolved">{t("resolvedOnly")}</SelectItem>
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
          {[...Array(5)].map((_, i) => (
            <Skeleton key={i} className="h-16 rounded-lg" />
          ))}
        </div>
      ) : incidents?.data && incidents.data.length > 0 ? (
        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("table.incident")}</TableHead>
                <TableHead>{t("table.check")}</TableHead>
                <TableHead className="w-10"></TableHead>
                <TableHead>{t("table.started")}</TableHead>
                <TableHead>{t("table.duration")}</TableHead>
                <TableHead>{t("table.failures")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {incidents.data.map((incident) => (
                <TableRow key={incident.uid} data-testid="incident-row">
                  <TableCell>
                    <div className="flex items-center gap-2">
                      <Link
                        to="/orgs/$org/incidents/$incidentUid"
                        params={{ org, incidentUid: incident.uid! }}
                        className="font-medium hover:underline"
                      >
                        {incident.title || incident.checkName || incident.checkSlug}
                      </Link>
                      {(incident.relapseCount ?? 0) > 0 && (
                        <Badge variant="outline" className="text-xs">
                          {t("relapse", { count: incident.relapseCount })}
                        </Badge>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    <Link
                      to="/orgs/$org/checks/$checkUid"
                      params={{ org, checkUid: incident.checkUid! }}
                      search={{ graphPeriod: undefined, graphFull: undefined }}
                      className="hover:underline text-sm text-muted-foreground"
                    >
                      {incident.checkSlug || incident.checkName}
                    </Link>
                  </TableCell>
                  <TableCell>
                    {incident.state === "active" ? (
                      <span title={t("active")}><AlertTriangle className="h-4 w-4 text-yellow-500" /></span>
                    ) : (
                      <span title={t("resolved")}><CheckCircle className="h-4 w-4 text-green-500" /></span>
                    )}
                  </TableCell>
                  <TableCell className="text-sm">
                    {incident.startedAt ? (
                      <span title={new Date(incident.startedAt).toLocaleString()}>
                        {formatRelativeTime(incident.startedAt)}
                      </span>
                    ) : "-"}
                  </TableCell>
                  <TableCell className="text-sm">
                    <IncidentDuration incident={incident} />
                  </TableCell>
                  <TableCell className="text-sm">
                    {incident.failureCount ?? "-"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : (
        <div className="text-center py-12 text-muted-foreground">
          <CheckCircle className="h-12 w-12 mx-auto mb-4 text-green-500 opacity-50" />
          <p className="text-lg font-medium">{t("noIncidentsFound")}</p>
          <p className="text-sm">
            {stateFilter === "active"
              ? t("allOperational")
              : stateFilter === "resolved"
                ? t("noResolved")
                : t("noIncidents")}
          </p>
        </div>
      )}
    </div>
  );
}
