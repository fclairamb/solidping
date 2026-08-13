// Counters on this page come from GET /orgs/{org}/checks/stats (useCheckStats),
// never from the checks list: the list endpoint clamps a page to 100 rows, so
// counting `checksQuery.data` was silently wrong for every org with more checks
// than that (GitHub issue #172). The checks query below is still issued, but
// only to render the "Checks at a glance" ROWS — never to derive a number.
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import {
  Activity,
  AlertTriangle,
  ArrowRight,
  CheckCircle,
  Clock,
  LayoutDashboard,
  ListChecks,
  RefreshCw,
} from "lucide-react";
import {
  useCheckStats,
  useChecks,
  useEvents,
  useIncidents,
  useResults,
  type Check,
  type CheckStats,
  type Event,
  type IncidentDetail,
  type OrgResult,
} from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";
import {
  CHECKS_LIST_POLL_MS,
  stretchWhileLive,
  useLiveSubscription,
  useScopeLive,
} from "@/contexts/LiveEventsContext";
import {
  Card,
  CardContent,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { UptimeStrip, type UptimeBucket } from "@/components/ui/uptime-strip";
import {
  getEventChannelName,
  getEventChannelUid,
  getEventCheckName,
  getEventDescription,
  getEventIcon,
  getEventLabel,
} from "@/components/dashboard/event-display";
import { MyOnCallWidget } from "@/components/dashboard/my-on-call";
import { EmptyStateOnboarding } from "@/components/dashboard/empty-state-onboarding";
import { PageHeader } from "@/components/shared/page-header";
import { StatusBadge } from "@/components/shared/status-badge";

const CHECK_POLL_MS = 30_000;
const INCIDENT_POLL_MS = 30_000;
const RESULT_POLL_MS = 60_000;
const EVENT_POLL_MS = 60_000;

// Max rows in the "Checks at a glance" card. The footer always links to the
// full /checks list so a capped card is never mistaken for the whole fleet.
const GLANCE_LIMIT = 10;
// Page size requested for the glance card's source list. The checks endpoint
// clamps `limit` to 100 server-side, so asking for more is a lie; the card is
// explicitly a sample and the counters come from the stats endpoint.
const GLANCE_PAGE_LIMIT = 100;

// Neutral snapshot used while the stats query is pending or has failed. Never
// a source of truth — just keeps the tiles from rendering `undefined`.
const EMPTY_CHECK_STATS: CheckStats = {
  total: 0,
  enabled: 0,
  disabled: 0,
  byStatus: {},
  down: 0,
  hardDown: 0,
};
// Hours rendered by each row's 24h uptime strip.
const UPTIME_HOURS = 24;

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

function effectiveStatus(check: Check): string | undefined {
  return check.status ?? check.lastResult?.status;
}

// The down / hard-down predicates that used to live here moved server-side to
// server/internal/handlers/checks/stats.go (isDownWireStatus /
// isHardDownWireStatus), where they are applied to the whole fleet instead of
// one page. Keeping a second copy here would let the two drift apart, which is
// exactly the failure mode the stats endpoint exists to prevent.

function isAttentionStatus(status?: string): boolean {
  return !!status && status !== "up" && status !== "created";
}

// Ordering for the glance card: checks needing attention first (status not
// up/created, most recent lastStatusChange first), then healthy enabled checks
// by name, then disabled checks last. Capped to GLANCE_LIMIT rows.
function orderChecksForGlance(checks: Check[]): Check[] {
  const byName = (a: Check, b: Check) =>
    (a.name || a.slug || a.uid).localeCompare(b.name || b.slug || b.uid);

  const attention = checks
    .filter((c) => isAttentionStatus(effectiveStatus(c)))
    .sort((a, b) => {
      const aTime = a.lastStatusChange?.time
        ? new Date(a.lastStatusChange.time).getTime()
        : 0;
      const bTime = b.lastStatusChange?.time
        ? new Date(b.lastStatusChange.time).getTime()
        : 0;
      return bTime - aTime;
    });

  const rest = checks.filter((c) => !isAttentionStatus(effectiveStatus(c)));
  const healthyEnabled = rest
    .filter((c) => c.enabled !== false)
    .sort(byName);
  const disabled = rest.filter((c) => c.enabled === false).sort(byName);

  return [...attention, ...healthyEnabled, ...disabled].slice(0, GLANCE_LIMIT);
}

interface CheckUptime {
  buckets: UptimeBucket[];
  latestDurationMs?: number;
}

// Group hourly OrgResults by checkUid into UPTIME_HOURS aligned hourly buckets
// (oldest → newest), plus the most recent bucket's latency. One pass over the
// single aggregated results query — no per-check work that scales with fleet.
function groupResultsByCheck(
  results: OrgResult[],
  now: number,
): Record<string, CheckUptime> {
  const hourMs = 60 * 60 * 1000;
  // Align the newest bucket to the current hour start so cells stay stable.
  const newestStart = Math.floor(now / hourMs) * hourMs;
  const slotForStart = (iso?: string): number => {
    if (!iso) return -1;
    const t = new Date(iso).getTime();
    if (Number.isNaN(t)) return -1;
    const slot = Math.floor((t - newestStart) / hourMs) + (UPTIME_HOURS - 1);
    return slot >= 0 && slot < UPTIME_HOURS ? slot : -1;
  };

  const byCheck: Record<string, OrgResult[]> = {};
  for (const r of results) {
    if (!r.checkUid) continue;
    (byCheck[r.checkUid] ||= []).push(r);
  }

  const out: Record<string, CheckUptime> = {};
  for (const [checkUid, rs] of Object.entries(byCheck)) {
    const cells: (OrgResult | undefined)[] = Array.from(
      { length: UPTIME_HOURS },
      () => undefined,
    );
    for (const r of rs) {
      const slot = slotForStart(r.periodStart);
      if (slot >= 0) cells[slot] = r;
    }
    const buckets: UptimeBucket[] = cells.map((cell, i) => ({
      periodStart:
        cell?.periodStart ??
        new Date(newestStart - (UPTIME_HOURS - 1 - i) * hourMs).toISOString(),
      availabilityPct: cell?.availabilityPct,
      durationMs: cell?.durationMs,
    }));
    // Latest response time = durationMs of the most recent bucket that has one.
    let latestDurationMs: number | undefined;
    for (let i = buckets.length - 1; i >= 0; i--) {
      if (buckets[i].durationMs !== undefined) {
        latestDurationMs = buckets[i].durationMs;
        break;
      }
    }
    out[checkUid] = { buckets, latestDurationMs };
  }
  return out;
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
  const { t: tNav } = useTranslation("nav");
  const { organizations } = useAuth();
  const orgName = organizations.find((o) => o.slug === org)?.name || org;
  // Collection subscriptions: the dashboard shows org-wide check membership,
  // status/results (via the `checks` scope), incidents, and the activity
  // feed. Each scope's own subscribed ack gates its poll stretch — a
  // rejected/unacked scope keeps polling at its base rate.
  useLiveSubscription({ entity: "checks" });
  useLiveSubscription({ entity: "incidents" });
  useLiveSubscription({ entity: "events" });
  const checksLive = useScopeLive({ entity: "checks" });
  const incidentsLive = useScopeLive({ entity: "incidents" });
  const eventsLive = useScopeLive({ entity: "events" });

  // Rows for the glance card only. `limit` matches the backend's hard clamp
  // rather than pretending to ask for more: past that the card is a sample of
  // the fleet, and its footer links to the full list. No counter is derived
  // from this query — see statsQuery below.
  const checksQuery = useChecks(org, {
    with: "last_result,last_status_change",
    limit: GLANCE_PAGE_LIMIT,
  });
  // Every KPI counter on this page. Server-side aggregate, so it is correct
  // regardless of how many checks the org has.
  const statsQuery = useCheckStats(org, {
    refetchInterval: stretchWhileLive(CHECK_POLL_MS, checksLive),
  });
  const incidentsQuery = useIncidents(org, {
    state: "active",
    size: 5,
    with: "check",
    hideSuppressed: true,
    refetchInterval: stretchWhileLive(INCIDENT_POLL_MS, incidentsLive),
  });
  const since24h = useMemo(
    () => new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
    [],
  );
  // Results ride the `checks` collection scope: v2 hints results/status
  // transitions to `checks` subscribers alongside membership changes.
  const resultsQuery = useResults(org, {
    periodType: "day",
    periodStartAfter: since24h,
    size: 1000,
    refetchInterval: stretchWhileLive(RESULT_POLL_MS, checksLive),
  });
  // One aggregated hourly query feeds every glance-card uptime strip — grouped
  // client-side by checkUid, so the card costs a single HTTP call regardless of
  // fleet size. The day query above keeps feeding the availability KPI.
  const hourlyResultsQuery = useResults(org, {
    periodType: "hour",
    periodStartAfter: since24h,
    size: 1000,
    refetchInterval: stretchWhileLive(RESULT_POLL_MS, checksLive),
  });
  const eventsQuery = useEvents(org, {
    size: 8,
    refetchInterval: stretchWhileLive(EVENT_POLL_MS, eventsLive),
  });

  // Manual polling for checks: useChecks doesn't expose refetchInterval. Use
  // a tick + refetch(). Deliberately NOT stretched while live: a `results`
  // hint no longer invalidates the checks-list roots (spec 2026-08-09-07), so
  // this tick is what keeps the glance card's per-run cells fresh between
  // status transitions — see CHECKS_LIST_POLL_MS.
  const checkTick = useTick(CHECKS_LIST_POLL_MS);
  useEffect(() => {
    if (checkTick) checksQuery.refetch();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [checkTick]);

  const checks = checksQuery.data || [];
  const incidents = incidentsQuery.data?.data || [];
  const results = resultsQuery.data?.data || [];
  const hourlyResults = hourlyResultsQuery.data?.data || [];
  const events = eventsQuery.data?.data || [];

  const isInitialLoading =
    checksQuery.isPending ||
    statsQuery.isPending ||
    incidentsQuery.isPending ||
    resultsQuery.isPending ||
    eventsQuery.isPending;

  // Every counter below reads the server-side aggregate. `stats` falls back to
  // an all-zero snapshot only while the query is pending or has errored — the
  // page renders skeletons in the pending case, and a failed stats query is
  // better shown as zeros than as a wrong page-derived number.
  const stats: CheckStats = statsQuery.data ?? EMPTY_CHECK_STATS;

  const isEmptyOrg = !statsQuery.isPending && stats.total === 0;

  const enabledCount = stats.enabled;
  const disabledCount = stats.disabled;
  const downCount = stats.down;
  const hardDownCount = stats.hardDown;
  const totalChecksCount = stats.total;
  const timeoutOnlyCount = downCount - hardDownCount;
  // The tile and banner need the true count of active incidents, not the
  // page length — the query below caps at `size: 5` for the "needs
  // attention" list, so `incidents.length` alone silently truncates at 5.
  const incidentsCount = incidentsQuery.data?.total ?? incidents.length;
  const availabilityPct = weightedAvailability(results);

  const glanceChecks = useMemo(() => orderChecksForGlance(checks), [checks]);
  const uptimeByCheck = useMemo(
    () => groupResultsByCheck(hourlyResults, Date.now()),
    [hourlyResults],
  );

  const refreshAll = () => {
    checksQuery.refetch();
    statsQuery.refetch();
    incidentsQuery.refetch();
    resultsQuery.refetch();
    hourlyResultsQuery.refetch();
    eventsQuery.refetch();
  };

  const isRefetching =
    checksQuery.isRefetching ||
    statsQuery.isRefetching ||
    incidentsQuery.isRefetching ||
    resultsQuery.isRefetching ||
    hourlyResultsQuery.isRefetching ||
    eventsQuery.isRefetching;

  const latestUpdate = Math.max(
    checksQuery.dataUpdatedAt || 0,
    statsQuery.dataUpdatedAt || 0,
    incidentsQuery.dataUpdatedAt || 0,
    resultsQuery.dataUpdatedAt || 0,
    hourlyResultsQuery.dataUpdatedAt || 0,
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
      <PageHeader
        icon={LayoutDashboard}
        title={tNav("dashboard")}
        description={
          <>
            <span className="font-medium text-foreground">{orgName}</span>
            {" — "}
            {t("subtitle")}
          </>
        }
        actions={
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
        }
        className="flex-wrap"
      />

      {isInitialLoading ? (
        <DashboardSkeleton />
      ) : isEmptyOrg ? (
        <EmptyStateOnboarding org={org} />
      ) : (
        <>
          <FirstResultCelebration org={org} checks={checks} totalChecks={totalChecksCount} />
          <OverallStatusBanner
            allGreen={downCount === 0 && incidentsCount === 0}
            hardDownCount={hardDownCount}
            timeoutOnlyCount={timeoutOnlyCount}
            incidentsCount={incidentsCount}
            checksCount={totalChecksCount}
            availabilityPct={availabilityPct}
          />

          <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
            <Link
              to="/orgs/$org/checks"
              params={{ org }}
              className="block"
              data-testid="kpi-tile-monitored"
            >
              <KpiTile
                label={t("kpi.monitored")}
                value={enabledCount}
                icon={<ListChecks className="h-4 w-4 text-muted-foreground" />}
                sub={
                  disabledCount > 0
                    ? t("kpi.monitoredDisabled", { count: disabledCount })
                    : undefined
                }
                className="transition hover:-translate-y-0.5 hover:bg-accent/40 hover:shadow-card-hover"
              />
            </Link>
            <Link
              to="/orgs/$org/checks"
              params={{ org }}
              search={{ status: "down" }}
              className="block"
              data-testid="kpi-tile-down"
            >
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
                className="transition hover:-translate-y-0.5 hover:bg-accent/40 hover:shadow-card-hover"
              />
            </Link>
            <Link
              to="/orgs/$org/incidents"
              params={{ org }}
              search={{ state: "active" as const, showSuppressed: undefined }}
              className="block"
              data-testid="kpi-tile-incidents"
            >
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
                className="transition hover:-translate-y-0.5 hover:bg-accent/40 hover:shadow-card-hover"
              />
            </Link>
          </div>

          {incidentsCount > 0 ? (
            <ActiveIncidentsList
              org={org}
              incidents={incidents}
              isError={!!incidentsQuery.error}
              onRetry={() => incidentsQuery.refetch()}
              tickNow={tickNow}
            />
          ) : null}

          <ChecksGlanceList
            org={org}
            checks={glanceChecks}
            totalCount={totalChecksCount}
            uptimeByCheck={uptimeByCheck}
            isError={!!checksQuery.error}
            onRetry={() => checksQuery.refetch()}
            tickNow={tickNow}
          />

          <RecentActivityList
            org={org}
            events={events}
            isError={!!eventsQuery.error}
            onRetry={() => eventsQuery.refetch()}
            tickNow={tickNow}
          />

          <MyOnCallWidget org={org} />
        </>
      )}
    </div>
  );
}

// FirstResultCelebration shows a one-shot banner the first time an org's
// first check has a result. Persisted in localStorage so it doesn't re-fire
// across reloads. Quiet on subsequent checks — this is an activation moment,
// not a per-check toast.
function FirstResultCelebration({
  org,
  checks,
  totalChecks,
}: {
  org: string;
  checks: Check[];
  /** Org-wide check count from the stats endpoint, not from `checks.length`. */
  totalChecks: number;
}) {
  const { t } = useTranslation("dashboard");
  const storageKey = `solidping_celebrated_first_result_${org}`;
  const [dismissed, setDismissed] = useState<boolean>(() => {
    if (typeof window === "undefined") return true;
    return window.localStorage.getItem(storageKey) === "1";
  });

  // Trigger only when there is a single check and it has at least one result
  // (lastResult is populated by the dashboard's checks-with-last-result query).
  const trigger =
    !dismissed && totalChecks === 1 && checks[0]?.lastResult !== undefined;
  const checkName = checks[0]?.name || "";

  useEffect(() => {
    if (trigger) {
      window.localStorage.setItem(storageKey, "1");
    }
  }, [trigger, storageKey]);

  if (!trigger) return null;

  return (
    <Card className="border-2 border-green-200 dark:border-green-800 bg-green-50 dark:bg-green-950/40">
      <CardContent className="pt-6 pb-6 flex items-center gap-4">
        <CheckCircle className="h-8 w-8 text-green-600 dark:text-green-500 shrink-0" />
        <div className="flex-1">
          <h2 className="text-lg font-semibold text-green-900 dark:text-green-100">
            {t("celebration.title", "First result is in!")}
          </h2>
          <p className="text-sm text-green-800/80 dark:text-green-300/80">
            {t("celebration.body", {
              defaultValue:
                "We just checked {{name}}. Hook up notifications so you'll know if it ever goes down.",
              name: checkName,
            })}
          </p>
        </div>
        <Button asChild size="sm">
          <a href={`/dash0/orgs/${org}/checks/${checks[0]?.uid}`}>
            {t("celebration.cta", "View the check")}
            <ArrowRight className="ml-1 h-3.5 w-3.5" />
          </a>
        </Button>
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setDismissed(true)}
          aria-label={t("celebration.dismiss", "Dismiss")}
        >
          ×
        </Button>
      </CardContent>
    </Card>
  );
}

function DashboardSkeleton() {
  return (
    <div className="space-y-6">
      <Skeleton className="h-12 rounded-xl" />
      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
        {[...Array(4)].map((_, i) => (
          <Skeleton key={i} className="h-28 rounded-xl" />
        ))}
      </div>
      <Skeleton className="h-96 rounded-xl" />
      <Skeleton className="h-72 rounded-xl" />
    </div>
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

  // Compact single-line strip: with real monitoring data now below it, the
  // banner is a headline (icon + title + inline sub), not the main content.
  if (allGreen) {
    return (
      <Card className="border-2 border-green-200 dark:border-green-800 bg-green-50 dark:bg-green-950/40">
        <CardContent className="py-3 flex flex-wrap items-center gap-x-3 gap-y-0.5">
          <CheckCircle className="h-5 w-5 text-green-600 dark:text-green-500 shrink-0" />
          <h2 className="text-base font-semibold text-green-900 dark:text-green-100">
            {t("banner.allGreen")}
          </h2>
          <p className="text-sm text-green-800/80 dark:text-green-300/80">
            {t("banner.allGreenSub", {
              count: checksCount,
              availability:
                availabilityPct === null ? "—" : availabilityPct.toFixed(2),
            })}
          </p>
        </CardContent>
      </Card>
    );
  }

  if (hardDownCount > 0 || incidentsCount > 0) {
    return (
      <Card className="border-2 border-red-200 dark:border-red-800 bg-red-50 dark:bg-red-950/40">
        <CardContent className="py-3 flex flex-wrap items-center gap-x-3 gap-y-0.5">
          <AlertTriangle className="h-5 w-5 text-red-600 dark:text-red-500 shrink-0" />
          <h2 className="text-base font-semibold text-red-900 dark:text-red-100">
            {t("banner.issues")}
          </h2>
          <p className="text-sm text-red-800/80 dark:text-red-300/80">
            {t("banner.issuesSub", {
              count: hardDownCount,
              down: hardDownCount,
              incidents: incidentsCount,
            })}
          </p>
        </CardContent>
      </Card>
    );
  }

  // Only timeouts (degraded but not hard-down).
  return (
    <Card className="border-2 border-yellow-200 dark:border-yellow-800 bg-yellow-50 dark:bg-yellow-950/40">
      <CardContent className="py-3 flex flex-wrap items-center gap-x-3 gap-y-0.5">
        <AlertTriangle className="h-5 w-5 text-yellow-600 dark:text-yellow-500 shrink-0" />
        <h2 className="text-base font-semibold text-yellow-900 dark:text-yellow-100">
          {t("banner.warning")}
        </h2>
        <p className="text-sm text-yellow-800/80 dark:text-yellow-300/80">
          {t("banner.warningSub", { count: timeoutOnlyCount })}
        </p>
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
  className?: string;
}

function KpiTile({ label, value, icon, sub, valueClassName, className }: KpiTileProps) {
  return (
    <Card className={className}>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-sm font-medium text-muted-foreground">
          {label}
        </CardTitle>
        {icon}
      </CardHeader>
      <CardContent>
        <div
          className={`text-3xl font-bold tracking-tight tabular-nums ${valueClassName || ""}`}
        >
          {value}
        </div>
        {sub ? (
          <p className="text-xs text-muted-foreground mt-1">{sub}</p>
        ) : null}
      </CardContent>
    </Card>
  );
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

interface ChecksGlanceListProps {
  org: string;
  checks: Check[];
  totalCount: number;
  uptimeByCheck: Record<string, CheckUptime>;
  isError: boolean;
  onRetry: () => void;
  tickNow: number;
}

function formatLatency(durationMs?: number): string {
  if (durationMs === undefined) return "—";
  return `${Math.round(durationMs)}ms`;
}

function ChecksGlanceList({
  org,
  checks,
  totalCount,
  uptimeByCheck,
  isError,
  onRetry,
  tickNow,
}: ChecksGlanceListProps) {
  const { t } = useTranslation("dashboard");

  return (
    <Card data-testid="checks-glance">
      <CardHeader>
        <CardTitle>{t("glance.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        {isError ? (
          <SectionError onRetry={onRetry} />
        ) : (
          <ul className="divide-y">
            {checks.map((check) => {
              const status = effectiveStatus(check);
              const isDisabled = check.enabled === false;
              const needsAttention = isAttentionStatus(status);
              const uptime = uptimeByCheck[check.uid];
              return (
                <li key={check.uid} data-testid="glance-row">
                  <Link
                    to="/orgs/$org/checks/$checkUid"
                    params={{ org, checkUid: check.uid }}
                    search={{ graphPeriod: undefined, graphFull: undefined, region: undefined }}
                    className={`flex items-center gap-3 py-3 hover:bg-accent/50 -mx-2 px-2 rounded transition-colors ${
                      isDisabled ? "opacity-60" : ""
                    }`}
                  >
                    <StatusBadge
                      status={status}
                      className="text-xs uppercase shrink-0"
                    />
                    <span className="font-medium truncate min-w-0 max-w-[40%] sm:max-w-[30%]">
                      {check.name || check.slug || check.uid}
                    </span>
                    {uptime ? (
                      <UptimeStrip
                        buckets={uptime.buckets}
                        className="hidden sm:flex flex-1 min-w-0"
                      />
                    ) : (
                      <span className="hidden sm:block flex-1" />
                    )}
                    <span className="text-xs tabular-nums text-muted-foreground shrink-0 ml-auto sm:ml-0">
                      {formatLatency(uptime?.latestDurationMs)}
                    </span>
                    {needsAttention && check.lastStatusChange?.time ? (
                      <span className="text-xs text-muted-foreground shrink-0">
                        {t("glance.since", {
                          time: formatRelative(
                            new Date(check.lastStatusChange.time),
                            tickNow,
                          ),
                        })}
                      </span>
                    ) : null}
                  </Link>
                </li>
              );
            })}
          </ul>
        )}
      </CardContent>
      <CardFooter>
        <Link
          to="/orgs/$org/checks"
          params={{ org }}
          className="text-sm text-primary hover:underline ml-auto inline-flex items-center gap-1"
          data-testid="checks-glance-footer"
        >
          {t("glance.footer", { count: totalCount })}
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
    <Card data-testid="active-incidents">
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
          search={{ state: "active" as const, showSuppressed: undefined }}
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
            {events.map((event) => {
              const description = getEventDescription(event.eventType, tEvents);
              const checkName = getEventCheckName(event);
              const channelName =
                event.eventType ===
                "org.activation.first_notification_configured"
                  ? getEventChannelName(event)
                  : undefined;
              const channelUid = channelName
                ? getEventChannelUid(event)
                : undefined;
              return (
                <li
                  key={event.uid}
                  className="flex items-center gap-3 py-3 text-sm"
                >
                  <span className="shrink-0">{getEventIcon(event.eventType)}</span>
                  <div className="flex-1 min-w-0">
                    <div className="truncate">
                      {getEventLabel(event.eventType, tEvents)}
                    </div>
                    {event.incidentUid || event.checkUid ? (
                      <div className="text-xs truncate flex items-center gap-1.5">
                        {event.incidentUid ? (
                          <Link
                            to="/orgs/$org/incidents/$incidentUid"
                            params={{ org, incidentUid: event.incidentUid }}
                            className="text-primary hover:underline"
                          >
                            {tEvents("links.incident")}
                          </Link>
                        ) : null}
                        {event.checkUid ? (
                          <Link
                            to="/orgs/$org/checks/$checkUid"
                            params={{ org, checkUid: event.checkUid }}
                            search={{ graphPeriod: undefined, graphFull: undefined, region: undefined }}
                            className="text-primary hover:underline"
                          >
                            {checkName ?? tEvents("links.check")}
                          </Link>
                        ) : checkName ? (
                          <span className="text-muted-foreground">
                            {checkName}
                          </span>
                        ) : null}
                      </div>
                    ) : channelName ? (
                      <div className="text-xs text-muted-foreground truncate">
                        {tEvents(
                          "descriptions.first_notification_configured_prefix",
                        )}{" "}
                        {channelUid ? (
                          <Link
                            to="/orgs/$org/integrations/$integrationUid"
                            params={{ org, integrationUid: channelUid }}
                            className="text-primary hover:underline"
                          >
                            {channelName}
                          </Link>
                        ) : (
                          <span>{channelName}</span>
                        )}
                      </div>
                    ) : description ? (
                      <div className="text-xs text-muted-foreground truncate">
                        {description}
                      </div>
                    ) : null}
                  </div>
                  {event.createdAt ? (
                    <span className="text-xs text-muted-foreground shrink-0">
                      {formatRelative(new Date(event.createdAt), tickNow)}
                    </span>
                  ) : null}
                </li>
              );
            })}
          </ul>
        )}
      </CardContent>
      <CardFooter>
        <Link
          to="/orgs/$org/events"
          params={{ org }}
          className="text-sm text-primary hover:underline ml-auto inline-flex items-center gap-1"
          data-testid="recent-activity-footer"
        >
          {t("recentActivity.footer")}
        </Link>
      </CardFooter>
    </Card>
  );
}
