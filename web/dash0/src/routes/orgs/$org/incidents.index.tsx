import { Fragment, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import {
  AlertTriangle,
  Check,
  CheckCircle,
  Layers,
  RefreshCw,
} from "lucide-react";
import {
  useCheckGroups,
  useChecks,
  useIncidents,
  type IncidentDetail,
} from "@/api/hooks";
import {
  groupHeaderCounts,
  groupIncidentsByCheckGroup,
  type IncidentGroupRow,
} from "@/lib/incident-grouping";
import { Badge, badgeVariants } from "@/components/ui/badge";
import { FlappingBadge } from "@/components/shared/flapping-badge";
import { IncidentKindChip } from "@/components/shared/incident-kind-chip";
import {
  INCIDENT_KINDS,
  incidentKindOf,
  incidentKindTextClass,
  type IncidentKind,
} from "@/lib/incident-kind";
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
import { useMediaQuery } from "@/hooks/use-media-query";
import { cn } from "@/lib/utils";

type StateFilter = "all" | "active" | "resolved" | "acked" | "snoozed";

const STATE_FILTER_VALUES: StateFilter[] = [
  "all",
  "active",
  "acked",
  "snoozed",
  "resolved",
];

interface IncidentsSearch {
  state: StateFilter;
  showSuppressed: true | undefined;
  checkUid: string | undefined;
  // Optional (not `| undefined`) so the many links into this page don't all
  // have to spell out `kind: undefined`.
  kind?: IncidentKind;
}

export const Route = createFileRoute("/orgs/$org/incidents/")({
  validateSearch: (search: Record<string, unknown>): IncidentsSearch => ({
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
    // Undefined when absent (every kind) so a clean URL stays clean.
    kind: INCIDENT_KINDS.includes(search.kind as IncidentKind)
      ? (search.kind as IncidentKind)
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

function useIncidentDuration(incident: IncidentDetail): string {
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
        new Date(incident.startedAt).getTime(),
    );
  }
  if (incident.startedAt) {
    return formatDuration(now - new Date(incident.startedAt).getTime());
  }
  return "-";
}

/**
 * "ongoing · 8m" in the kind's colour while the incident is open, "✓ lasted
 * 48m" in muted green once it resolved.
 */
function IncidentWhen({ incident }: { incident: IncidentDetail }) {
  const { t } = useTranslation("incidents");
  const duration = useIncidentDuration(incident);

  if (incident.state === "active" && !incident.resolvedAt) {
    return (
      <span
        className={cn("font-medium", incidentKindTextClass(incident.kind))}
        data-testid="incident-when"
      >
        {t("when.ongoing", { duration })}
      </span>
    );
  }

  return (
    <span
      className="inline-flex items-center gap-1 text-emerald-700/80 dark:text-emerald-400/80"
      data-testid="incident-when"
    >
      <Check className="h-3 w-3 shrink-0" aria-hidden="true" />
      {t("when.lasted", { duration })}
    </span>
  );
}

/**
 * The "escalated" badge on a degraded incident that ended because a real
 * outage opened on the same check. When that outage is on the loaded page
 * (its causedByIncidentUid points back here) the badge names it and links to
 * it; otherwise it is a plain badge.
 */
function EscalatedBadge({
  org,
  target,
}: {
  org: string;
  target: IncidentDetail | undefined;
}) {
  const { t } = useTranslation("incidents");
  const className = "text-xs font-normal whitespace-nowrap";

  if (target?.uid && target.number) {
    return (
      <Link
        to="/orgs/$org/incidents/$incidentUid"
        params={{ org, incidentUid: target.uid }}
        title={t("escalatedHint")}
        className={cn(
          badgeVariants({ variant: "outline" }),
          className,
          incidentKindTextClass(target.kind),
          "hover:underline",
        )}
        data-testid="incident-escalated-badge"
      >
        {t("escalatedTo", { number: target.number })}
      </Link>
    );
  }

  return (
    <Badge
      variant="outline"
      className={className}
      title={t("escalatedHint")}
      data-testid="incident-escalated-badge"
    >
      {t("escalated")}
    </Badge>
  );
}

/**
 * A plain, non-interactive grouping header: "RabbitMQ — 2/6 down", with the
 * group's member incidents listed beneath it.
 *
 * Deliberately NOT collapsible. There is no per-group open/closed state worth
 * persisting across filters, sorts and pagination, and every other list in
 * this app that groups rows without a persisted collapse state renders a plain
 * header the same way. The accepted trade-off is that a large group takes more
 * vertical space with no way to fold it away.
 */
function GroupHeaderRow({ row }: { row: IncidentGroupRow }) {
  const { t } = useTranslation("incidents");
  const { down, total } = groupHeaderCounts(row);

  return (
    <TableRow
      className="bg-muted/40 hover:bg-muted/40"
      data-testid="incident-group-header"
      data-check-group-uid={row.group!.uid}
    >
      <TableCell colSpan={3} className="py-2">
        <div className="flex items-center gap-2 text-sm">
          <Layers className="h-4 w-4 text-muted-foreground shrink-0" />
          <span className="font-semibold">
            {total === undefined
              ? t("group.header", { name: row.group!.name, down })
              : t("group.headerWithTotal", {
                  name: row.group!.name,
                  down,
                  total,
                })}
          </span>
        </div>
      </TableCell>
    </TableRow>
  );
}

function IncidentsIndexPage() {
  const { t } = useTranslation("incidents");
  const { org } = Route.useParams();
  // Read directly off the route's search state — no local-state mirror —
  // so a cold deep-link (?checkUid=... pasted into a fresh tab) applies on
  // first render instead of only after a client-side navigation seeds it.
  const {
    state: stateFilter,
    showSuppressed,
    checkUid,
    kind: kindFilter,
  } = Route.useSearch();
  const navigate = useNavigate();
  // Below `sm` the start time moves from the When cell to the end of the
  // row's first line. Rendering it once, in the right place, keeps a single
  // `incident-started-at` per row.
  const isSmUp = useMediaQuery("(min-width: 640px)");

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
    kind: kindFilter,
    checkUid: checkUid || undefined,
    size: 50,
    with: "check",
    hideSuppressed: !showSuppressed,
  });

  // Read-time aggregation (spec 2026-08-24-14). Incidents are per-check, so
  // the "RabbitMQ — 2/6 down" consolidation that group incidents used to bake
  // into the row is rebuilt here from two cheap, already-cached queries: the
  // checks map checkUid → group, the groups supply the name and member count.
  // `GET /incidents` is untouched — it stays a flat list, as every other list
  // endpoint here does.
  //
  // The 100-check page limit is the API's own clamp: past it, an incident
  // whose check did not load renders as an ordinary ungrouped row rather than
  // under a header with a wrong denominator.
  const { data: checks } = useChecks(org, { limit: 100 });
  const { data: checkGroups } = useCheckGroups(org);

  const rows = groupIncidentsByCheckGroup(incidents?.data, checks, checkGroups);

  // An escalated degraded incident links to the outage that superseded it:
  // that outage's causedByIncidentUid points back at the degraded one. Only
  // what is on the loaded page can be named.
  const causedBy = new Map<string, IncidentDetail>();
  for (const inc of incidents?.data ?? []) {
    if (inc.causedByIncidentUid) causedBy.set(inc.causedByIncidentUid, inc);
  }

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
            <SelectTrigger
              className="w-[180px]"
              data-testid="incidents-state-filter"
            >
              <SelectValue placeholder={t("filterByState")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("allIncidents")}</SelectItem>
              <SelectItem value="active">{t("activeOnly")}</SelectItem>
              <SelectItem value="acked">
                {t("stateFilter.ackedOnly")}
              </SelectItem>
              <SelectItem value="snoozed">
                {t("stateFilter.snoozedOnly")}
              </SelectItem>
              <SelectItem value="resolved">{t("resolvedOnly")}</SelectItem>
            </SelectContent>
          </Select>
          <Select
            value={kindFilter ?? "all"}
            onValueChange={(v) =>
              navigate({
                to: ".",
                search: (prev) => ({
                  ...prev,
                  kind: v === "all" ? undefined : (v as IncidentKind),
                }),
                replace: true,
              })
            }
          >
            <SelectTrigger
              className="w-[160px]"
              data-testid="incidents-kind-filter"
            >
              <SelectValue placeholder={t("kindFilter.placeholder")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("kindFilter.all")}</SelectItem>
              {INCIDENT_KINDS.map((kind) => (
                <SelectItem key={kind} value={kind}>
                  {t(`kind.${kind}`)}
                </SelectItem>
              ))}
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
                <TableHead className="w-px whitespace-nowrap">
                  {t("table.when")}
                </TableHead>
                <TableHead className="hidden md:table-cell w-px whitespace-nowrap text-right">
                  {t("table.failures")}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row) => (
                <Fragment key={row.key}>
                  {row.group ? <GroupHeaderRow row={row} /> : null}
                  {row.incidents.map((incident) => {
                    const startedAt = incident.startedAt ? (
                      <TimeAgo
                        date={incident.startedAt}
                        data-testid="incident-started-at"
                      />
                    ) : (
                      "-"
                    );

                    return (
                      <TableRow
                        key={incident.uid}
                        data-testid="incident-row"
                        data-incident-uid={incident.uid}
                        data-incident-kind={incidentKindOf(incident.kind)}
                        className="hover:bg-muted/40 transition-colors"
                      >
                        <TableCell className="align-top">
                          <div className="flex flex-wrap items-center gap-2">
                            <IncidentKindChip
                              kind={incident.kind}
                              state={incident.state}
                            />
                            {incident.number ? (
                              <Link
                                to="/orgs/$org/incidents/$incidentUid"
                                params={{ org, incidentUid: incident.uid! }}
                                className="font-mono text-xs text-muted-foreground font-semibold px-1.5 py-0.5 rounded bg-muted shrink-0 hover:text-foreground transition-colors"
                                data-testid="incident-number"
                              >
                                #{incident.number}
                              </Link>
                            ) : null}
                            {incident.state === "active" &&
                              incident.snoozedUntil &&
                              new Date(incident.snoozedUntil).getTime() >
                                Date.now() && (
                                <Badge
                                  variant="outline"
                                  className="text-xs font-normal"
                                >
                                  {t("stateBadges.snoozed")}
                                </Badge>
                              )}
                            {incident.state === "active" &&
                              incident.acknowledgedAt &&
                              (!incident.snoozedUntil ||
                                new Date(incident.snoozedUntil).getTime() <=
                                  Date.now()) && (
                                <Badge
                                  variant="outline"
                                  className="text-xs font-normal bg-amber-500/10 text-amber-700 dark:text-amber-400 border-amber-500/20"
                                >
                                  {t("stateBadges.acked")}
                                </Badge>
                              )}
                            {(incident.relapseCount ?? 0) > 0 && (
                              <Badge
                                variant="outline"
                                className="text-xs font-normal"
                              >
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
                              <Badge
                                variant="outline"
                                className="text-xs font-normal"
                              >
                                {t("rollup.rolledUpBadge")}
                              </Badge>
                            )}
                            {incident.state !== "active" &&
                              incident.resolutionType === "escalated" && (
                                <EscalatedBadge
                                  org={org}
                                  target={causedBy.get(incident.uid!)}
                                />
                              )}
                            {!isSmUp && (
                              <span className="ml-auto text-xs text-muted-foreground font-mono whitespace-nowrap">
                                {startedAt}
                              </span>
                            )}
                          </div>
                          <Link
                            to="/orgs/$org/incidents/$incidentUid"
                            params={{ org, incidentUid: incident.uid! }}
                            className="mt-1 block font-medium text-foreground hover:text-primary hover:underline transition-colors [overflow-wrap:anywhere]"
                            data-testid="incident-title"
                          >
                            {incident.title ||
                              incident.checkName ||
                              incident.checkSlug}
                          </Link>
                          {incident.checkUid ? (
                            <Link
                              to="/orgs/$org/checks/$checkUid"
                              params={{ org, checkUid: incident.checkUid }}
                              search={{
                                graphPeriod: undefined,
                                graphFull: undefined,
                                region: undefined,
                              }}
                              className="mt-0.5 hidden sm:block w-fit max-w-full text-xs text-muted-foreground hover:text-foreground hover:underline transition-colors [overflow-wrap:anywhere]"
                              data-testid="incident-check-link"
                            >
                              {incident.checkName || incident.checkSlug}
                            </Link>
                          ) : null}
                        </TableCell>
                        <TableCell className="w-px whitespace-nowrap align-top text-xs font-mono">
                          {isSmUp && (
                            <div className="text-muted-foreground">
                              {startedAt}
                            </div>
                          )}
                          <div className={isSmUp ? "mt-1" : undefined}>
                            <IncidentWhen incident={incident} />
                          </div>
                        </TableCell>
                        <TableCell className="hidden md:table-cell w-px whitespace-nowrap align-top text-right text-xs text-muted-foreground font-mono tabular-nums">
                          {incident.failureCount ?? "-"}
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </Fragment>
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
            <h3 className="font-semibold text-base text-foreground">
              {t("noIncidentsFound")}
            </h3>
            <p className="text-xs text-muted-foreground max-w-sm mx-auto">
              {kindFilter
                ? t(`noIncidentsOfKind.${kindFilter}`)
                : checkUid
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
