// Counters on this page come from GET /orgs/{org}/checks/stats (useCheckStats),
// never from the checks list: the list endpoint clamps a page to 100 rows, so
// counting `checksQuery.data` was silently wrong for every org with more checks
// than that (GitHub issue #172). The checks query below is still issued, but
// only to render the "Checks at a glance" ROWS — never to derive a number.
import { Fragment, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import {
  Activity,
  AlertTriangle,
  ArrowRight,
  CheckCircle,
  Clock,
  Layers,
  LayoutDashboard,
  ListChecks,
  RefreshCw,
  TrendingUp,
} from "lucide-react";
import {
  useCheckGroups,
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
import {
  availabilityTier,
  type AvailabilityTier,
} from "@/lib/availability-tier";
import {
  groupHeaderCounts,
  groupIncidentsByCheckGroup,
  type IncidentGroupRow,
} from "@/lib/incident-grouping";
import { formatIssuesSubtitle } from "@/lib/issues-banner";
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
  CardDescription,
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
import { OnboardingChecklist } from "@/components/dashboard/onboarding-checklist";
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
  availability24h: null,
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
  const healthyEnabled = rest.filter((c) => c.enabled !== false).sort(byName);
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
      // Carried so the strip can apply the shared small-bucket guard (one failed
      // sample is never red) instead of classifying on the percentage alone.
      totalChecks: cell?.totalChecks,
      successfulChecks: cell?.successfulChecks,
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

// Tailwind classes per tier, matching the emerald/amber/destructive/muted
// conventions the other KPI tiles on this page already use. The tier logic
// itself (thresholds, availabilityTier) lives in lib/availability-tier.ts —
// a pure, directly unit-testable module, and split out so this file's export
// surface stays component-only (react-refresh/only-export-components).
const AVAILABILITY_TIER_CLASSES: Record<
  AvailabilityTier,
  { badge: string; icon: string }
> = {
  noData: {
    badge: "text-muted-foreground bg-muted",
    icon: "text-muted-foreground",
  },
  operational: {
    badge: "text-emerald-600 dark:text-emerald-400 bg-emerald-500/10",
    icon: "text-emerald-500",
  },
  degraded: {
    badge: "text-amber-600 dark:text-amber-400 bg-amber-500/10",
    icon: "text-amber-500",
  },
  down: {
    badge: "text-destructive bg-destructive/10",
    icon: "text-destructive",
  },
};

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
  // One aggregated hourly query feeds every glance-card uptime strip — grouped
  // client-side by checkUid, so the card costs a single HTTP call regardless
  // of fleet size. The 24h availability KPI does NOT ride this query (or any
  // client-side results query): hour rows only exist once raw rows age past
  // retention_raw, so the newest hour row typically lags ~24h behind and the
  // trailing day lives almost entirely in raw rows the dashboard must never
  // pull fleet-wide. It comes from the server-computed
  // stats.availability24h instead (see statsQuery below).
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
  const hourlyResults = hourlyResultsQuery.data?.data || [];
  const events = eventsQuery.data?.data || [];

  const isInitialLoading =
    checksQuery.isPending ||
    statsQuery.isPending ||
    incidentsQuery.isPending ||
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
  // Server-computed (spec 2026-08-26-09) — see CheckStats.availability24h.
  const availabilityPct = stats.availability24h;
  const availabilityTierValue = availabilityTier(availabilityPct);
  const availabilityTierClasses =
    AVAILABILITY_TIER_CLASSES[availabilityTierValue];

  const glanceChecks = useMemo(() => orderChecksForGlance(checks), [checks]);
  const uptimeByCheck = useMemo(
    () => groupResultsByCheck(hourlyResults, Date.now()),
    [hourlyResults],
  );

  const refreshAll = () => {
    checksQuery.refetch();
    statsQuery.refetch();
    incidentsQuery.refetch();
    hourlyResultsQuery.refetch();
    eventsQuery.refetch();
  };

  const isRefetching =
    checksQuery.isRefetching ||
    statsQuery.isRefetching ||
    incidentsQuery.isRefetching ||
    hourlyResultsQuery.isRefetching ||
    eventsQuery.isRefetching;

  const latestUpdate = Math.max(
    checksQuery.dataUpdatedAt || 0,
    statsQuery.dataUpdatedAt || 0,
    incidentsQuery.dataUpdatedAt || 0,
    hourlyResultsQuery.dataUpdatedAt || 0,
    eventsQuery.dataUpdatedAt || 0,
  );
  const tickNow = useTick(1000);
  const updatedLabel =
    latestUpdate > 0
      ? tickNow - latestUpdate < 5_000
        ? t("justUpdated")
        : t("updatedAgo", {
            time: formatRelative(new Date(latestUpdate), tickNow),
          })
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
              <RefreshCw
                className={`h-4 w-4 ${isRefetching ? "animate-spin" : ""}`}
              />
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
          <OnboardingChecklist
            org={org}
            totalChecks={totalChecksCount}
            firstCheckUid={checks[0]?.uid}
          />
          <OverallStatusBanner
            org={org}
            allGreen={downCount === 0 && incidentsCount === 0}
            hardDownCount={hardDownCount}
            timeoutOnlyCount={timeoutOnlyCount}
            incidentsCount={incidentsCount}
            checksCount={totalChecksCount}
            availabilityPct={availabilityPct}
          />

          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Link
              to="/orgs/$org/checks"
              params={{ org }}
              className="block"
              data-testid="kpi-tile-monitored"
            >
              <KpiTile
                label={t("kpi.monitored")}
                value={enabledCount}
                icon={<ListChecks className="h-4 w-4 text-primary" />}
                badge={
                  <span className="text-[11px] font-medium text-emerald-600 dark:text-emerald-400 bg-emerald-500/10 px-2 py-0.5 rounded-full">
                    {enabledCount} Active
                  </span>
                }
                sub={
                  disabledCount > 0
                    ? t("kpi.monitoredDisabled", { count: disabledCount })
                    : `${totalChecksCount} total endpoints`
                }
                className="transition hover:-translate-y-0.5 hover:shadow-card-hover"
              />
            </Link>
            <div className="block" data-testid="kpi-tile-availability">
              <KpiTile
                label="24h Availability"
                value={
                  availabilityPct === null
                    ? "—"
                    : `${availabilityPct.toFixed(2)}%`
                }
                icon={
                  <TrendingUp
                    className={`h-4 w-4 ${availabilityTierClasses.icon}`}
                  />
                }
                badge={
                  <span
                    className={`text-[11px] font-medium px-2 py-0.5 rounded-full ${availabilityTierClasses.badge}`}
                  >
                    {t(`kpi.availabilityBadge.${availabilityTierValue}`)}
                  </span>
                }
                sub={
                  availabilityPct === null
                    ? t("kpi.availabilityNoDataSub")
                    : "Fleet uptime health"
                }
                className="transition hover:-translate-y-0.5 hover:shadow-card-hover"
              />
            </div>
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
                    className={`h-4 w-4 ${downCount > 0 ? "text-destructive" : "text-muted-foreground"}`}
                  />
                }
                badge={
                  downCount > 0 ? (
                    <span className="text-[11px] font-medium text-destructive bg-destructive/10 px-2 py-0.5 rounded-full animate-pulse">
                      Needs Action
                    </span>
                  ) : (
                    <span className="text-[11px] font-medium text-emerald-600 dark:text-emerald-400 bg-emerald-500/10 px-2 py-0.5 rounded-full">
                      All Up
                    </span>
                  )
                }
                valueClassName={downCount > 0 ? "text-destructive" : undefined}
                sub={downCount === 0 ? t("kpi.downSubNone") : undefined}
                className="transition hover:-translate-y-0.5 hover:shadow-card-hover"
              />
            </Link>
            <Link
              to="/orgs/$org/incidents"
              params={{ org }}
              search={{
                state: "active" as const,
                showSuppressed: undefined,
                checkUid: undefined,
              }}
              className="block"
              data-testid="kpi-tile-incidents"
            >
              <KpiTile
                label={t("kpi.incidents")}
                value={incidentsCount}
                icon={
                  <Activity
                    className={`h-4 w-4 ${incidentsCount > 0 ? "text-amber-500" : "text-muted-foreground"}`}
                  />
                }
                badge={
                  incidentsCount > 0 ? (
                    <span className="text-[11px] font-medium text-amber-600 dark:text-amber-400 bg-amber-500/10 px-2 py-0.5 rounded-full">
                      Active
                    </span>
                  ) : (
                    <span className="text-[11px] font-medium text-emerald-600 dark:text-emerald-400 bg-emerald-500/10 px-2 py-0.5 rounded-full">
                      Clean
                    </span>
                  )
                }
                valueClassName={
                  incidentsCount > 0
                    ? "text-amber-600 dark:text-amber-500"
                    : undefined
                }
                sub={
                  incidentsCount === 0 ? t("kpi.incidentsSubNone") : undefined
                }
                className="transition hover:-translate-y-0.5 hover:shadow-card-hover"
              />
            </Link>
          </div>

          {incidentsCount > 0 ? (
            <ActiveIncidentsList
              org={org}
              incidents={incidents}
              checks={checks}
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
  org: string;
  allGreen: boolean;
  hardDownCount: number;
  timeoutOnlyCount: number;
  incidentsCount: number;
  checksCount: number;
  availabilityPct: number | null;
}

// Wraps the banner's content in the TanStack Router `<Link>` matching its
// destination — incidents-first, since an active incident carries the
// ack/snooze/resolve workflow while a down check without one is just a
// state. A real `<Link>` (not a `div` + onClick) keeps keyboard focus,
// middle-click and copy-link working. `data-testid="overall-status-banner"`
// lives here so existing E2E assertions keep working unchanged.
function BannerLink({
  org,
  target,
  className,
  children,
}: {
  org: string;
  target: "incidents-active" | "checks-down" | "checks-warning";
  className: string;
  children: React.ReactNode;
}) {
  if (target === "incidents-active") {
    return (
      <Link
        to="/orgs/$org/incidents"
        params={{ org }}
        search={{
          state: "active" as const,
          showSuppressed: undefined,
          checkUid: undefined,
        }}
        data-testid="overall-status-banner"
        className={className}
      >
        {children}
      </Link>
    );
  }
  return (
    <Link
      to="/orgs/$org/checks"
      params={{ org }}
      search={{ status: target === "checks-down" ? "down" : "warning" }}
      data-testid="overall-status-banner"
      className={className}
    >
      {children}
    </Link>
  );
}

function OverallStatusBanner({
  org,
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
      <div
        data-testid="overall-status-banner"
        className="relative overflow-hidden rounded-xl border border-emerald-500/20 bg-gradient-to-r from-emerald-500/[0.08] via-green-500/[0.03] to-transparent p-3.5 sm:p-4 shadow-sm"
      >
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <span className="relative flex h-3 w-3 shrink-0">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
              <span className="relative inline-flex rounded-full h-3 w-3 bg-emerald-500" />
            </span>
            <div className="flex flex-wrap items-baseline gap-2">
              <h2 className="text-sm sm:text-base font-semibold text-foreground">
                {t("banner.allGreen")}
              </h2>
              <span className="text-xs text-muted-foreground">
                {availabilityPct === null
                  ? t("banner.allGreenSubNoData", { count: checksCount })
                  : t("banner.allGreenSub", {
                      count: checksCount,
                      availability: availabilityPct.toFixed(2),
                    })}
              </span>
            </div>
          </div>
          {/* The "24h SLA Operational" pill asserts a real SLA figure — with
              no data yet there is nothing honest to claim, so it is
              suppressed rather than shown against a fabricated number. */}
          {availabilityPct !== null ? (
            <div className="flex items-center gap-1.5 text-xs font-medium text-emerald-700 dark:text-emerald-400 bg-emerald-500/10 px-2.5 py-1 rounded-full border border-emerald-500/20">
              <CheckCircle className="h-3.5 w-3.5" />
              <span>24h SLA Operational</span>
            </div>
          ) : null}
        </div>
      </div>
    );
  }

  if (hardDownCount > 0 || incidentsCount > 0) {
    return (
      <BannerLink
        org={org}
        target={incidentsCount > 0 ? "incidents-active" : "checks-down"}
        className="block"
      >
        <div className="relative overflow-hidden rounded-xl border border-destructive/30 bg-destructive/10 p-3.5 sm:p-4 shadow-sm cursor-pointer transition hover:border-destructive/50 hover:bg-destructive/15">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-center gap-3">
              <span className="relative flex h-3 w-3 shrink-0">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-destructive opacity-75" />
                <span className="relative inline-flex rounded-full h-3 w-3 bg-destructive" />
              </span>
              <div className="flex flex-wrap items-baseline gap-2">
                <h2 className="text-sm sm:text-base font-semibold text-destructive">
                  {t("banner.issues")}
                </h2>
                <span className="text-xs text-muted-foreground">
                  {formatIssuesSubtitle(t, hardDownCount, incidentsCount)}
                </span>
              </div>
            </div>
            <div className="flex items-center gap-1.5 text-xs font-medium text-destructive bg-destructive/15 px-2.5 py-1 rounded-full border border-destructive/30">
              <AlertTriangle className="h-3.5 w-3.5" />
              <span>Active Outage</span>
            </div>
          </div>
        </div>
      </BannerLink>
    );
  }

  // Only timeouts (degraded but not hard-down).
  return (
    <BannerLink org={org} target="checks-warning" className="block">
      <div className="relative overflow-hidden rounded-xl border border-amber-500/30 bg-amber-500/10 p-3.5 sm:p-4 shadow-sm cursor-pointer transition hover:border-amber-500/50 hover:bg-amber-500/15">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <AlertTriangle className="h-4 w-4 text-amber-600 dark:text-amber-500 shrink-0" />
            <div className="flex flex-wrap items-baseline gap-2">
              <h2 className="text-sm sm:text-base font-semibold text-amber-900 dark:text-amber-100">
                {t("banner.warning")}
              </h2>
              <span className="text-xs text-amber-800/80 dark:text-amber-300/80">
                {t("banner.warningSub", { count: timeoutOnlyCount })}
              </span>
            </div>
          </div>
          <div className="flex items-center gap-1.5 text-xs font-medium text-amber-700 dark:text-amber-400 bg-amber-500/15 px-2.5 py-1 rounded-full border border-amber-500/30">
            <span>Degraded Performance</span>
          </div>
        </div>
      </div>
    </BannerLink>
  );
}

interface KpiTileProps {
  label: string;
  value: number | string;
  icon: React.ReactNode;
  sub?: string;
  badge?: React.ReactNode;
  valueClassName?: string;
  className?: string;
}

function KpiTile({
  label,
  value,
  icon,
  sub,
  badge,
  valueClassName,
  className,
}: KpiTileProps) {
  return (
    <Card className={className}>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-xs font-semibold tracking-wider text-muted-foreground uppercase">
          {label}
        </CardTitle>
        <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-muted/60 text-muted-foreground">
          {icon}
        </div>
      </CardHeader>
      <CardContent className="space-y-1">
        <div className="flex items-baseline justify-between gap-2">
          <div
            className={`text-2xl sm:text-3xl font-bold tracking-tight tabular-nums ${valueClassName || "text-foreground"}`}
          >
            {value}
          </div>
          {badge}
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
      <CardHeader className="flex flex-row items-center justify-between py-4">
        <div>
          <CardTitle className="text-base font-semibold">
            {t("glance.title")}
          </CardTitle>
          <CardDescription className="text-xs text-muted-foreground mt-0.5">
            Fleet health preview and live response telemetry
          </CardDescription>
        </div>
        <Button
          asChild
          variant="ghost"
          size="sm"
          className="text-xs font-medium gap-1 text-primary"
        >
          <Link to="/orgs/$org/checks" params={{ org }}>
            View all ({totalCount}) <ArrowRight className="h-3 w-3" />
          </Link>
        </Button>
      </CardHeader>
      <CardContent className="p-0">
        {isError ? (
          <div className="p-6">
            <SectionError onRetry={onRetry} />
          </div>
        ) : (
          <div className="divide-y border-t">
            {checks.map((check) => {
              const status = effectiveStatus(check);
              const isDisabled = check.enabled === false;
              const needsAttention = isAttentionStatus(status);
              const uptime = uptimeByCheck[check.uid];
              const latencyMs =
                uptime?.latestDurationMs ?? check.lastResult?.durationMs;
              const checkType = check.type || "http";

              return (
                <div key={check.uid} data-testid="glance-row" className="group">
                  <Link
                    to="/orgs/$org/checks/$checkUid"
                    params={{ org, checkUid: check.uid }}
                    search={{
                      graphPeriod: undefined,
                      graphFull: undefined,
                      region: undefined,
                    }}
                    className={`flex items-center gap-3 px-4 sm:px-6 py-3 hover:bg-accent/40 transition-colors ${
                      isDisabled ? "opacity-60" : ""
                    }`}
                  >
                    <div className="flex items-center gap-2 shrink-0">
                      <span className="relative flex h-2 w-2">
                        {!isDisabled && status === "up" && (
                          <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-60" />
                        )}
                        <span
                          className={`relative inline-flex rounded-full h-2 w-2 ${
                            isDisabled
                              ? "bg-muted-foreground/50"
                              : status === "up"
                                ? "bg-emerald-500"
                                : status === "warning"
                                  ? "bg-amber-500"
                                  : "bg-destructive"
                          }`}
                        />
                      </span>
                      <StatusBadge
                        status={status}
                        className="text-[10px] font-semibold uppercase px-1.5 py-0.5 tracking-wide shrink-0"
                      />
                    </div>

                    <div className="min-w-0 flex-1 sm:max-w-[34%]">
                      <div className="flex items-center gap-2">
                        <span className="font-medium text-sm text-foreground truncate group-hover:text-primary transition-colors">
                          {check.name || check.slug || check.uid}
                        </span>
                        <span className="hidden md:inline-flex text-[10px] font-mono uppercase px-1.5 py-0.5 rounded bg-muted text-muted-foreground shrink-0">
                          {checkType}
                        </span>
                      </div>
                    </div>

                    <div className="hidden sm:flex flex-1 items-center px-2 min-w-0">
                      {uptime?.buckets && uptime.buckets.length > 0 ? (
                        <UptimeStrip
                          buckets={uptime.buckets}
                          className="w-full"
                        />
                      ) : (
                        <div className="h-4 w-full rounded bg-muted/40 flex items-center justify-center">
                          <span className="text-[10px] text-muted-foreground/60 font-mono">
                            24h telemetry active
                          </span>
                        </div>
                      )}
                    </div>

                    <div className="flex items-center gap-2.5 shrink-0 ml-auto sm:ml-0">
                      {latencyMs !== undefined ? (
                        <span
                          className={`text-xs font-mono tabular-nums px-2 py-0.5 rounded-md font-medium ${
                            latencyMs < 50
                              ? "bg-emerald-500/10 text-emerald-700 dark:text-emerald-300 border border-emerald-500/20"
                              : latencyMs < 250
                                ? "bg-sky-500/10 text-sky-700 dark:text-sky-300 border border-sky-500/20"
                                : "bg-amber-500/10 text-amber-700 dark:text-amber-300 border border-amber-500/20"
                          }`}
                        >
                          {Math.round(latencyMs)}ms
                        </span>
                      ) : (
                        <span className="text-xs font-mono text-muted-foreground">
                          —
                        </span>
                      )}

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
                    </div>
                  </Link>
                </div>
              );
            })}
          </div>
        )}
      </CardContent>
      <CardFooter className="py-2.5 px-6 border-t bg-muted/20">
        <Link
          to="/orgs/$org/checks"
          params={{ org }}
          className="text-xs font-medium text-muted-foreground hover:text-foreground ml-auto inline-flex items-center gap-1 transition-colors"
          data-testid="checks-glance-footer"
        >
          {t("glance.footer", { count: totalCount })}{" "}
          <ArrowRight className="h-3 w-3 ml-0.5" />
        </Link>
      </CardFooter>
    </Card>
  );
}

/**
 * The plain, non-interactive grouping header label — "RabbitMQ — 2/6 down".
 *
 * Not collapsible, by decision: there is no per-group open/closed state worth
 * persisting across filters and pagination, and the incidents list renders the
 * same header the same way.
 */
function groupHeaderLabel(
  row: IncidentGroupRow,
  t: (key: string, opts: Record<string, unknown>) => string,
): string {
  const { down, total } = groupHeaderCounts(row);

  return total === undefined
    ? t("group.header", { name: row.group!.name, down })
    : t("group.headerWithTotal", { name: row.group!.name, down, total });
}

interface ActiveIncidentsListProps {
  org: string;
  incidents: IncidentDetail[];
  /** Reused from the page's own query purely to map checkUid → check group. */
  checks: Check[];
  isError: boolean;
  onRetry: () => void;
  tickNow: number;
}

function ActiveIncidentsList({
  org,
  incidents,
  checks,
  isError,
  onRetry,
  tickNow,
}: ActiveIncidentsListProps) {
  const { t } = useTranslation("dashboard");
  const { t: tIncidents } = useTranslation("incidents");

  // Read-time aggregation (spec 2026-08-24-14): incidents are per-check, and
  // the "RabbitMQ — 2/6 down" consolidation is rebuilt here rather than baked
  // into the row. Same grouping helper the incidents list uses, so the two
  // surfaces cannot drift.
  const { data: checkGroups } = useCheckGroups(org);
  const rows = groupIncidentsByCheckGroup(incidents, checks, checkGroups);

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
            {rows.map((row) => (
              <Fragment key={row.key}>
                {row.group ? (
                  <li
                    className="flex items-center gap-2 pt-3 pb-1 text-xs font-semibold text-muted-foreground"
                    data-testid="incident-group-header"
                    data-check-group-uid={row.group.uid}
                  >
                    <Layers className="h-3.5 w-3.5 shrink-0" />
                    <span>{groupHeaderLabel(row, tIncidents)}</span>
                  </li>
                ) : null}
                {row.incidents.map((incident) => (
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
                          {formatRelative(
                            new Date(incident.startedAt),
                            tickNow,
                          )}
                        </span>
                      ) : null}
                    </Link>
                  </li>
                ))}
              </Fragment>
            ))}
          </ul>
        )}
      </CardContent>
      <CardFooter>
        <Link
          to="/orgs/$org/incidents"
          params={{ org }}
          search={{
            state: "active" as const,
            showSuppressed: undefined,
            checkUid: undefined,
          }}
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
                  <span className="shrink-0">
                    {getEventIcon(event.eventType)}
                  </span>
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
                            search={{
                              graphPeriod: undefined,
                              graphFull: undefined,
                              region: undefined,
                            }}
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
