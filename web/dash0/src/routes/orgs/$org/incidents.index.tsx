import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { AlertTriangle, CheckCircle, RefreshCw } from "lucide-react";
import { useIncidents, type IncidentDetail } from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
import { FlappingBadge } from "@/components/shared/flapping-badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { TimeAgo } from "@/components/ui/time-ago";
import { Switch } from "@/components/ui/switch";
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
import { CheckPicker } from "@/components/shared/check-picker";
import { useLiveSubscription } from "@/contexts/LiveEventsContext";

type StateFilter = "all" | "active" | "resolved" | "acked" | "snoozed";

const STATE_FILTER_VALUES: StateFilter[] = [
  "all",
  "active",
  "acked",
  "snoozed",
  "resolved",
];

export const Route = createFileRoute("/orgs/$org/incidents/")({
  validateSearch: (search: Record<string, unknown>) => ({
    state: (STATE_FILTER_VALUES.includes(search.state as StateFilter)
      ? search.state
      : "all") as StateFilter,
    // TanStack Router's default search parser already coerces "true"/"false"
    // query-string values to native booleans before validateSearch runs, so a
    // bare `=== "true"` string comparison silently always evaluates to false
    // (see login.tsx's `session_expired` fix for the same bug class).
    showSuppressed:
      search.showSuppressed === true || search.showSuppressed === "true"
        ? true
        : undefined,
    // Undefined when absent so a clean URL stays clean. Accepts a uid OR a
    // slug — the backend resolves both (issue #127) — so an operator can
    // type a human-readable value by hand.
    checkUid:
      typeof search.checkUid === "string" && search.checkUid
        ? search.checkUid
        : undefined,
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

function IncidentsIndexPage() {
  const { t } = useTranslation("incidents");
  const { org } = Route.useParams();
  // Read directly off the route's search state — no local-state mirror —
  // so a cold deep-link (?checkUid=... pasted into a fresh tab) applies on
  // first render instead of only after a client-side navigation seeds it.
  const { state: stateFilter, showSuppressed, checkUid } = Route.useSearch();
  const navigate = useNavigate();

  // Live updates: an `incidents` hint invalidates the `incidents` org root
  // (DEFAULT_QUERY_ROOTS), whose predicate ignores the options segment — so
  // every `state`/`showSuppressed` filter variant of useIncidents refetches.
  useLiveSubscription({ entity: "incidents" });

  const {
    data: incidents,
    isLoading,
    error,
    refetch,
    isRefetching,
  } = useIncidents(org, {
    state: stateFilter === "all" ? undefined : stateFilter,
    checkUid: checkUid || undefined,
    size: 50,
    with: "check",
    hideSuppressed: !showSuppressed,
  });

  return (
    <div className="space-y-6">
      <PageHeader
        icon={AlertTriangle}
        title={t("title")}
        description={t("subtitle")}
        docsHref="/docs/features/incidents"
        className="flex-wrap"
      />

      <div className="flex flex-wrap items-center justify-between gap-4">
        <div className="flex flex-wrap items-center gap-3">
          <Select
            value={stateFilter}
            onValueChange={(v) =>
              navigate({
                to: ".",
                // Functional form: carries every other filter forward so a
                // fourth one can't reintroduce the "picking a state drops
                // the check filter" bug.
                search: (prev) => ({ ...prev, state: v as StateFilter }),
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
              <SelectItem value="acked">{t("stateFilter.ackedOnly")}</SelectItem>
              <SelectItem value="snoozed">{t("stateFilter.snoozedOnly")}</SelectItem>
              <SelectItem value="resolved">{t("resolvedOnly")}</SelectItem>
            </SelectContent>
          </Select>
          <div className="w-[220px] max-w-full">
            <CheckPicker
              org={org}
              value={checkUid}
              onChange={(uid) =>
                navigate({
                  to: ".",
                  search: (prev) => ({ ...prev, checkUid: uid }),
                  replace: true,
                })
              }
              placeholder={t("filterByCheck")}
              triggerTestId="incidents-check-filter"
            />
          </div>
          <label className="flex items-center gap-2 text-sm text-muted-foreground hover:text-foreground cursor-pointer">
            <Switch
              checked={!!showSuppressed}
              onCheckedChange={(checked) =>
                navigate({
                  to: ".",
                  search: (prev) => ({
                    ...prev,
                    showSuppressed: checked ? true : undefined,
                  }),
                  replace: true,
                })
              }
              data-testid="incidents-show-suppressed-toggle"
            />
            <span>{t("rollup.showRolledUp")}</span>
          </label>
        </div>
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
          {[...Array(5)].map((_, i) => (
            <Skeleton key={i} className="h-16 rounded-lg" />
          ))}
        </div>
      ) : incidents?.data && incidents.data.length > 0 ? (
        <div className="rounded-xl border bg-card shadow-card overflow-hidden">
          <Table>
            <TableHeader className="bg-muted/30">
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
                <TableRow
                  key={incident.uid}
                  data-testid="incident-row"
                  data-incident-uid={incident.uid}
                  className="hover:bg-muted/40 transition-colors"
                >
                  <TableCell>
                    <div className="flex items-center gap-2">
                      {incident.number ? (
                        <span
                          className="font-mono text-xs text-muted-foreground font-semibold px-1.5 py-0.5 rounded bg-muted"
                          data-testid="incident-number"
                        >
                          #{incident.number}
                        </span>
                      ) : null}
                      <Link
                        to="/orgs/$org/incidents/$incidentUid"
                        params={{ org, incidentUid: incident.uid! }}
                        className="font-medium text-foreground hover:text-primary hover:underline transition-colors"
                      >
                        {incident.title || incident.checkName || incident.checkSlug}
                      </Link>
                      {incident.state === "active" &&
                        incident.snoozedUntil &&
                        new Date(incident.snoozedUntil).getTime() > Date.now() && (
                          <Badge variant="outline" className="text-xs font-normal">
                            {t("stateBadges.snoozed")}
                          </Badge>
                        )}
                      {incident.state === "active" &&
                        incident.acknowledgedAt &&
                        (!incident.snoozedUntil ||
                          new Date(incident.snoozedUntil).getTime() <= Date.now()) && (
                          <Badge variant="outline" className="text-xs font-normal bg-amber-500/10 text-amber-700 dark:text-amber-400 border-amber-500/20">
                            {t("stateBadges.acked")}
                          </Badge>
                        )}
                      {(incident.relapseCount ?? 0) > 0 && (
                        <Badge variant="outline" className="text-xs font-normal">
                          {t("relapse", { count: incident.relapseCount })}
                        </Badge>
                      )}
                      {(incident.flapLevel ?? 0) > 0 && (
                        <FlappingBadge
                          flapLevel={incident.flapLevel!}
                          t={t}
                          className="text-xs font-normal"
                        />
                      )}
                      {incident.pagingSuppressed && (
                        <Badge variant="outline" className="text-xs font-normal">
                          {t("rollup.rolledUpBadge")}
                        </Badge>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    <Link
                      to="/orgs/$org/checks/$checkUid"
                      params={{ org, checkUid: incident.checkUid! }}
                      search={{ graphPeriod: undefined, graphFull: undefined, region: undefined }}
                      className="hover:underline text-sm font-medium text-muted-foreground hover:text-foreground transition-colors"
                    >
                      {incident.checkSlug || incident.checkName}
                    </Link>
                  </TableCell>
                  <TableCell>
                    {incident.state === "active" ? (
                      <span title={t("active")} className="relative flex h-3 w-3">
                        <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-destructive opacity-75" />
                        <span className="relative inline-flex rounded-full h-3 w-3 bg-destructive" />
                      </span>
                    ) : (
                      <span title={t("resolved")} className="flex h-2.5 w-2.5 rounded-full bg-emerald-500" />
                    )}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground font-mono">
                    {incident.startedAt ? (
                      <TimeAgo
                        date={incident.startedAt}
                        data-testid="incident-started-at"
                      />
                    ) : "-"}
                  </TableCell>
                  <TableCell className="text-xs font-mono font-medium">
                    <IncidentDuration incident={incident} />
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground font-mono tabular-nums">
                    {incident.failureCount ?? "-"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      ) : (
        <div className="rounded-xl border bg-card p-12 text-center shadow-card space-y-3">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-emerald-500/10 text-emerald-600 dark:text-emerald-400">
            <CheckCircle className="h-6 w-6" />
          </div>
          <div className="space-y-1">
            <h3 className="font-semibold text-base text-foreground">{t("noIncidentsFound")}</h3>
            <p className="text-xs text-muted-foreground max-w-sm mx-auto">
              {checkUid
                ? t("noIncidentsForCheck")
                : stateFilter === "active"
                  ? t("allOperational")
                  : stateFilter === "resolved"
                    ? t("noResolved")
                    : t("noIncidents")}
            </p>
          </div>
        </div>
      )}
    </div>
  );
}
