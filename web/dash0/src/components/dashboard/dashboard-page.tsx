// TODO(perf): switch to GET /api/v1/orgs/{org}/dashboard once a backend
// aggregate endpoint exists. For orgs > 1000 checks this fetches the full
// list just to count — fine for now (typical org has <100 checks).
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import {
  Activity,
  AlertTriangle,
  ArrowRight,
  CheckCircle,
  Clock,
  ListChecks,
  Plus,
  RefreshCw,
} from "lucide-react";
import {
  useChecks,
  useEvents,
  useIncidents,
  useResults,
  type Check,
  type Event,
  type IncidentDetail,
  type OrgResult,
} from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";
import {
  Card,
  CardContent,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  getEventIcon,
  getEventLabel,
} from "@/components/dashboard/event-display";

const CHECK_POLL_MS = 30_000;
const INCIDENT_POLL_MS = 30_000;
const RESULT_POLL_MS = 60_000;
const EVENT_POLL_MS = 60_000;

interface OrgDashboardPageProps {
  org: string;
}

function formatRelative(date: Date, now: number): string {
  const diffSec = Math.max(0, Math.floor((now - date.getTime()) / 1000));
  if (diffSec < 60) return `${diffSec}s`;
  const diffMin = Math.floor(diffSec / 60);
  if (diffMin < 60) return `${diffMin}m`;
  const diffHour = Math.floor(diffMin / 60);
  if (diffHour < 24) return `${diffHour}h`;
  return `${Math.floor(diffHour / 24)}d`;
}

function useTick(intervalMs: number) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

function isDownStatus(status?: string): boolean {
  return status === "down" || status === "error" || status === "timeout";
}

function isHardDownStatus(status?: string): boolean {
  return status === "down" || status === "error";
}

function pickTopAttention(checks: Check[]): Check[] {
  return [...checks]
    .filter((c) => c.lastResult?.status && c.lastResult.status !== "up")
    .sort((a, b) => {
      const aTime = a.lastStatusChange?.time
        ? new Date(a.lastStatusChange.time).getTime()
        : 0;
      const bTime = b.lastStatusChange?.time
        ? new Date(b.lastStatusChange.time).getTime()
        : 0;
      return bTime - aTime;
    })
    .slice(0, 5);
}

function weightedAvailability(results: OrgResult[]): number | null {
  let totalChecks = 0;
  let successfulChecks = 0;
  for (const r of results) {
    if (typeof r.totalChecks === "number" && typeof r.successfulChecks === "number") {
      totalChecks += r.totalChecks;
      successfulChecks += r.successfulChecks;
    }
  }
  if (totalChecks === 0) return null;
  return (successfulChecks / totalChecks) * 100;
}

export function OrgDashboardPage({ org }: OrgDashboardPageProps) {
  const { t } = useTranslation("dashboard");
  const { organizations } = useAuth();
  const orgName = organizations.find((o) => o.slug === org)?.name || org;

  const checksQuery = useChecks(org, {
    with: "last_result,last_status_change",
    limit: 1000,
  });
  const incidentsQuery = useIncidents(org, {
    state: "active",
    size: 5,
    with: "check",
    refetchInterval: INCIDENT_POLL_MS,
  });
  const since24h = useMemo(
    () => new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
    [],
  );
  const resultsQuery = useResults(org, {
    periodType: "day",
    periodStartAfter: since24h,
    size: 1000,
    refetchInterval: RESULT_POLL_MS,
  });
  const eventsQuery = useEvents(org, {
    size: 8,
    refetchInterval: EVENT_POLL_MS,
  });

  // Manual polling for checks: useChecks doesn't expose refetchInterval. Use
  // a tick + refetch().
  const checkTick = useTick(CHECK_POLL_MS);
  useEffect(() => {
    if (checkTick) checksQuery.refetch();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [checkTick]);

  const checks = checksQuery.data || [];
  const incidents = incidentsQuery.data?.data || [];
  const results = resultsQuery.data?.data || [];
  const events = eventsQuery.data?.data || [];

  const isInitialLoading =
    checksQuery.isLoading &&
    incidentsQuery.isLoading &&
    resultsQuery.isLoading &&
    eventsQuery.isLoading;

  const isEmptyOrg = !checksQuery.isLoading && checks.length === 0;

  const enabledCount = checks.filter((c) => c.enabled !== false).length;
  const disabledCount = checks.length - enabledCount;
  const downCount = checks.filter((c) => isDownStatus(c.lastResult?.status)).length;
  const hardDownCount = checks.filter((c) =>
    isHardDownStatus(c.lastResult?.status),
  ).length;
  const timeoutOnlyCount = downCount - hardDownCount;
  const incidentsCount = incidents.length;
  const availabilityPct = weightedAvailability(results);

  const refreshAll = () => {
    checksQuery.refetch();
    incidentsQuery.refetch();
    resultsQuery.refetch();
    eventsQuery.refetch();
  };

  const isRefetching =
    checksQuery.isRefetching ||
    incidentsQuery.isRefetching ||
    resultsQuery.isRefetching ||
    eventsQuery.isRefetching;

  const latestUpdate = Math.max(
    checksQuery.dataUpdatedAt || 0,
    incidentsQuery.dataUpdatedAt || 0,
    resultsQuery.dataUpdatedAt || 0,
    eventsQuery.dataUpdatedAt || 0,
  );
  const tickNow = useTick(1000);
  const updatedLabel =
    latestUpdate > 0
      ? tickNow - latestUpdate < 5_000
        ? t("justUpdated")
        : t("updatedAgo", { time: formatRelative(new Date(latestUpdate), tickNow) })
      : "";

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{orgName}</h1>
          <p className="text-muted-foreground">{t("subtitle")}</p>
        </div>
        <div className="flex items-center gap-3 text-sm text-muted-foreground">
          {updatedLabel ? <span>{updatedLabel}</span> : null}
          <Button
            variant="outline"
            size="icon"
            onClick={refreshAll}
            disabled={isRefetching}
            aria-label={t("refresh")}
          >
            <RefreshCw className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`} />
          </Button>
        </div>
      </div>

      {isInitialLoading ? (
        <DashboardSkeleton />
      ) : isEmptyOrg ? (
        <EmptyOrgWelcome org={org} />
      ) : (
        <>
          <OverallStatusBanner
            allGreen={downCount === 0 && incidentsCount === 0}
            hardDownCount={hardDownCount}
            timeoutOnlyCount={timeoutOnlyCount}
            incidentsCount={incidentsCount}
            checksCount={checks.length}
            availabilityPct={availabilityPct}
          />

          <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
            <KpiTile
              label={t("kpi.monitored")}
              value={enabledCount}
              icon={<ListChecks className="h-4 w-4 text-muted-foreground" />}
              sub={
                disabledCount > 0
                  ? t("kpi.monitoredDisabled", { count: disabledCount })
                  : undefined
              }
            />
            <KpiTile
              label={t("kpi.down")}
              value={downCount}
              icon={
                <AlertTriangle
                  className={`h-4 w-4 ${downCount > 0 ? "text-red-600" : "text-muted-foreground"}`}
                />
              }
              valueClassName={downCount > 0 ? "text-red-600 dark:text-red-500" : undefined}
              sub={downCount === 0 ? t("kpi.downSubNone") : undefined}
            />
            <KpiTile
              label={t("kpi.incidents")}
              value={incidentsCount}
              icon={
                <Activity
                  className={`h-4 w-4 ${incidentsCount > 0 ? "text-yellow-600" : "text-muted-foreground"}`}
                />
              }
              valueClassName={incidentsCount > 0 ? "text-yellow-600 dark:text-yellow-500" : undefined}
              sub={incidentsCount === 0 ? t("kpi.incidentsSubNone") : undefined}
            />
            <KpiTile
              label={t("kpi.availability")}
              value={availabilityPct === null ? "—" : `${availabilityPct.toFixed(2)}%`}
              icon={<CheckCircle className="h-4 w-4 text-muted-foreground" />}
              sub={availabilityPct === null ? t("kpi.availabilityNoData") : undefined}
            />
          </div>

          <div className="grid gap-6 lg:grid-cols-2">
            <NeedsAttentionList
              org={org}
              checks={pickTopAttention(checks)}
              isError={!!checksQuery.error}
              onRetry={() => checksQuery.refetch()}
              tickNow={tickNow}
            />
            <ActiveIncidentsList
              org={org}
              incidents={incidents}
              isError={!!incidentsQuery.error}
              onRetry={() => incidentsQuery.refetch()}
              tickNow={tickNow}
            />
          </div>

          <RecentActivityList
            org={org}
            events={events}
            isError={!!eventsQuery.error}
            onRetry={() => eventsQuery.refetch()}
            tickNow={tickNow}
          />
        </>
      )}
    </div>
  );
}

function DashboardSkeleton() {
  return (
    <div className="space-y-6">
      <Skeleton className="h-24 rounded-xl" />
      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
        {[...Array(4)].map((_, i) => (
          <Skeleton key={i} className="h-28 rounded-xl" />
        ))}
      </div>
      <div className="grid gap-6 lg:grid-cols-2">
        <Skeleton className="h-72 rounded-xl" />
        <Skeleton className="h-72 rounded-xl" />
      </div>
      <Skeleton className="h-72 rounded-xl" />
    </div>
  );
}

function EmptyOrgWelcome({ org }: { org: string }) {
  const { t } = useTranslation("dashboard");
  return (
    <Card className="border-2 border-blue-200 dark:border-blue-800 bg-blue-50/50 dark:bg-blue-950/30">
      <CardContent className="pt-6 flex flex-col items-center text-center gap-4 py-12">
        <div className="rounded-full bg-blue-100 dark:bg-blue-900 p-4">
          <Plus className="h-8 w-8 text-blue-600 dark:text-blue-400" />
        </div>
        <div>
          <h2 className="text-2xl font-bold">{t("welcome.title")}</h2>
          <p className="text-muted-foreground mt-1">{t("welcome.subtitle")}</p>
        </div>
        <Button asChild size="lg">
          <Link
            to="/orgs/$org/checks/new"
            params={{ org }}
            search={{
              checkType: undefined,
              checkPeriod: undefined,
              checkName: undefined,
              checkSlug: undefined,
              httpUrl: undefined,
              httpMethod: undefined,
              host: undefined,
              port: undefined,
              url: undefined,
              domain: undefined,
              username: undefined,
              database: undefined,
            }}
          >
            {t("welcome.cta")}
          </Link>
        </Button>
      </CardContent>
    </Card>
  );
}

interface OverallStatusBannerProps {
  allGreen: boolean;
  hardDownCount: number;
  timeoutOnlyCount: number;
  incidentsCount: number;
  checksCount: number;
  availabilityPct: number | null;
}

function OverallStatusBanner({
  allGreen,
  hardDownCount,
  timeoutOnlyCount,
  incidentsCount,
  checksCount,
  availabilityPct,
}: OverallStatusBannerProps) {
  const { t } = useTranslation("dashboard");

  if (allGreen) {
    return (
      <Card className="border-2 border-green-200 dark:border-green-800 bg-green-50 dark:bg-green-950/40">
        <CardContent className="pt-6 flex items-center gap-4">
          <CheckCircle className="h-8 w-8 text-green-600 dark:text-green-500 shrink-0" />
          <div>
            <h2 className="text-xl font-semibold text-green-900 dark:text-green-100">
              {t("banner.allGreen")}
            </h2>
            <p className="text-sm text-green-800/80 dark:text-green-300/80">
              {t("banner.allGreenSub", {
                count: checksCount,
                availability:
                  availabilityPct === null ? "—" : availabilityPct.toFixed(2),
              })}
            </p>
          </div>
        </CardContent>
      </Card>
    );
  }

  if (hardDownCount > 0 || incidentsCount > 0) {
    return (
      <Card className="border-2 border-red-200 dark:border-red-800 bg-red-50 dark:bg-red-950/40">
        <CardContent className="pt-6 flex items-center gap-4">
          <AlertTriangle className="h-8 w-8 text-red-600 dark:text-red-500 shrink-0" />
          <div>
            <h2 className="text-xl font-semibold text-red-900 dark:text-red-100">
              {t("banner.issues")}
            </h2>
            <p className="text-sm text-red-800/80 dark:text-red-300/80">
              {t("banner.issuesSub", {
                count: hardDownCount,
                down: hardDownCount,
                incidents: incidentsCount,
              })}
            </p>
          </div>
        </CardContent>
      </Card>
    );
  }

  // Only timeouts (degraded but not hard-down).
  return (
    <Card className="border-2 border-yellow-200 dark:border-yellow-800 bg-yellow-50 dark:bg-yellow-950/40">
      <CardContent className="pt-6 flex items-center gap-4">
        <AlertTriangle className="h-8 w-8 text-yellow-600 dark:text-yellow-500 shrink-0" />
        <div>
          <h2 className="text-xl font-semibold text-yellow-900 dark:text-yellow-100">
            {t("banner.warning")}
          </h2>
          <p className="text-sm text-yellow-800/80 dark:text-yellow-300/80">
            {t("banner.warningSub", { count: timeoutOnlyCount })}
          </p>
        </div>
      </CardContent>
    </Card>
  );
}

interface KpiTileProps {
  label: string;
  value: number | string;
  icon: React.ReactNode;
  sub?: string;
  valueClassName?: string;
}

function KpiTile({ label, value, icon, sub, valueClassName }: KpiTileProps) {
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-sm font-medium text-muted-foreground">
          {label}
        </CardTitle>
        {icon}
      </CardHeader>
      <CardContent>
        <div className={`text-3xl font-bold ${valueClassName || ""}`}>{value}</div>
        {sub ? (
          <p className="text-xs text-muted-foreground mt-1">{sub}</p>
        ) : null}
      </CardContent>
    </Card>
  );
}

function statusBadgeVariant(
  status?: string,
): "default" | "destructive" | "secondary" | "outline" {
  if (status === "down" || status === "error") return "destructive";
  if (status === "timeout") return "secondary";
  return "outline";
}

interface SectionErrorProps {
  onRetry: () => void;
}

function SectionError({ onRetry }: SectionErrorProps) {
  const { t } = useTranslation("dashboard");
  return (
    <div className="flex items-center justify-between rounded-md border border-dashed p-4 text-sm text-muted-foreground">
      <span>{t("errors.section")}</span>
      <Button variant="outline" size="sm" onClick={onRetry}>
        {t("errors.retry")}
      </Button>
    </div>
  );
}

interface NeedsAttentionListProps {
  org: string;
  checks: Check[];
  isError: boolean;
  onRetry: () => void;
  tickNow: number;
}

function NeedsAttentionList({
  org,
  checks,
  isError,
  onRetry,
  tickNow,
}: NeedsAttentionListProps) {
  const { t } = useTranslation("dashboard");

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("needsAttention.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        {isError ? (
          <SectionError onRetry={onRetry} />
        ) : checks.length === 0 ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground py-6 justify-center">
            <CheckCircle className="h-5 w-5 text-green-500" />
            <span>{t("needsAttention.empty")}</span>
          </div>
        ) : (
          <ul className="divide-y">
            {checks.map((check) => (
              <li key={check.uid}>
                <Link
                  to="/orgs/$org/checks/$checkUid"
                  params={{ org, checkUid: check.uid }}
                  search={{ graphPeriod: undefined, graphFull: undefined }}
                  className="flex items-center justify-between gap-3 py-3 hover:bg-accent/50 -mx-2 px-2 rounded transition-colors"
                >
                  <div className="flex items-center gap-3 min-w-0">
                    <Badge
                      variant={statusBadgeVariant(check.lastResult?.status)}
                      className="text-xs uppercase"
                    >
                      {check.lastResult?.status || "?"}
                    </Badge>
                    <span className="font-medium truncate">
                      {check.name || check.slug || check.uid}
                    </span>
                  </div>
                  {check.lastStatusChange?.time ? (
                    <span className="text-xs text-muted-foreground shrink-0">
                      {t("needsAttention.since", {
                        time: formatRelative(
                          new Date(check.lastStatusChange.time),
                          tickNow,
                        ),
                      })}
                    </span>
                  ) : null}
                </Link>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
      <CardFooter>
        <Link
          to="/orgs/$org/checks"
          params={{ org }}
          className="text-sm text-primary hover:underline ml-auto inline-flex items-center gap-1"
        >
          {t("needsAttention.footer")}
        </Link>
      </CardFooter>
    </Card>
  );
}

interface ActiveIncidentsListProps {
  org: string;
  incidents: IncidentDetail[];
  isError: boolean;
  onRetry: () => void;
  tickNow: number;
}

function ActiveIncidentsList({
  org,
  incidents,
  isError,
  onRetry,
  tickNow,
}: ActiveIncidentsListProps) {
  const { t } = useTranslation("dashboard");

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("activeIncidents.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        {isError ? (
          <SectionError onRetry={onRetry} />
        ) : incidents.length === 0 ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground py-6 justify-center">
            <CheckCircle className="h-5 w-5 text-green-500" />
            <span>{t("activeIncidents.empty")}</span>
          </div>
        ) : (
          <ul className="divide-y">
            {incidents.map((incident) => (
              <li key={incident.uid}>
                <Link
                  to="/orgs/$org/incidents/$incidentUid"
                  params={{ org, incidentUid: incident.uid! }}
                  className="flex items-center justify-between gap-3 py-3 hover:bg-accent/50 -mx-2 px-2 rounded transition-colors"
                >
                  <div className="flex items-center gap-3 min-w-0">
                    <AlertTriangle className="h-4 w-4 text-red-500 shrink-0" />
                    <div className="min-w-0">
                      <div className="font-medium truncate">
                        {incident.title || t("activeIncidents.untitled")}
                      </div>
                      {incident.checkName ? (
                        <div className="text-xs text-muted-foreground truncate">
                          {incident.checkName}
                        </div>
                      ) : null}
                    </div>
                  </div>
                  {incident.startedAt ? (
                    <span className="flex items-center gap-1 text-xs text-muted-foreground shrink-0">
                      <Clock className="h-3 w-3" />
                      {formatRelative(new Date(incident.startedAt), tickNow)}
                    </span>
                  ) : null}
                </Link>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
      <CardFooter>
        <Link
          to="/orgs/$org/incidents"
          params={{ org }}
          search={{ state: "active" }}
          className="text-sm text-primary hover:underline ml-auto inline-flex items-center gap-1"
        >
          {t("activeIncidents.footer")}
        </Link>
      </CardFooter>
    </Card>
  );
}

interface RecentActivityListProps {
  org: string;
  events: Event[];
  isError: boolean;
  onRetry: () => void;
  tickNow: number;
}

function RecentActivityList({
  org,
  events,
  isError,
  onRetry,
  tickNow,
}: RecentActivityListProps) {
  const { t } = useTranslation("dashboard");
  const { t: tEvents } = useTranslation("events");

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("recentActivity.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        {isError ? (
          <SectionError onRetry={onRetry} />
        ) : events.length === 0 ? (
          <div className="text-sm text-muted-foreground py-6 text-center">
            {t("recentActivity.empty")}
          </div>
        ) : (
          <ul className="divide-y">
            {events.map((event) => (
              <li
                key={event.uid}
                className="flex items-center gap-3 py-3 text-sm"
              >
                <span className="shrink-0">{getEventIcon(event.eventType)}</span>
                <span className="flex-1 truncate">
                  {getEventLabel(event.eventType, tEvents)}
                </span>
                {event.createdAt ? (
                  <span className="text-xs text-muted-foreground shrink-0">
                    {formatRelative(new Date(event.createdAt), tickNow)}
                  </span>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
      <CardFooter>
        <Link
          to="/orgs/$org/events"
          params={{ org }}
          className="text-sm text-primary hover:underline ml-auto inline-flex items-center gap-1"
        >
          {t("recentActivity.footer")}
          <ArrowRight className="h-3 w-3" />
        </Link>
      </CardFooter>
    </Card>
  );
}
