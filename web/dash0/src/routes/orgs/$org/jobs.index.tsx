import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Activity, RefreshCw, Workflow } from "lucide-react";

import { useAuth } from "@/contexts/AuthContext";
import { Button } from "@/components/ui/button";
import { SegmentedControl } from "@/components/ui/segmented-control";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { TimeAgoOrDash } from "@/components/ui/time-ago";
import { PageHeader } from "@/components/shared/page-header";
import { StatTile, type StatTileTone } from "@/components/shared/stat-tile";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  useJobsStats,
  useBackgroundJobs,
  useCheckSchedule,
  useRegions,
  type BackgroundJob,
  type CheckJobState,
  type CheckScheduleJob,
} from "@/api/hooks";
import { regionDisplayLabel } from "@/lib/region-label";

const PAGE_SIZE = 50;

const JOB_STATUSES = ["pending", "running", "success", "retried", "failed"] as const;
const JOB_TYPES = [
  "sleep",
  "email",
  "webhook",
  "startup",
  "aggregation",
  "state_cleanup",
  "notification",
  "snooze_sweep",
  "escalation_step",
  "network_discovery",
  "network_discovery_plan",
  "freebox_lan_discovery",
] as const;

const JOB_TABS = ["jobs", "schedule"] as const;
type JobsTab = (typeof JOB_TABS)[number];

type JobsSearch = {
  tab: JobsTab;
  allOrgs?: true;
  status?: string;
  type?: string;
};

export const Route = createFileRoute("/orgs/$org/jobs/")({
  validateSearch: (search: Record<string, unknown>): JobsSearch => ({
    tab: JOB_TABS.includes(search.tab as JobsTab) ? (search.tab as JobsTab) : "jobs",
    allOrgs: search.allOrgs === true || search.allOrgs === "true" ? true : undefined,
    status: (JOB_STATUSES as readonly string[]).includes(search.status as string)
      ? (search.status as string)
      : undefined,
    type: (JOB_TYPES as readonly string[]).includes(search.type as string)
      ? (search.type as string)
      : undefined,
  }),
  component: JobsIndexPage,
});

type BadgeVariant = "default" | "secondary" | "destructive" | "outline" | "success" | "warning";

function jobStatusVariant(status: string): BadgeVariant {
  switch (status) {
    case "success":
      return "success";
    case "running":
      return "default";
    case "pending":
      return "secondary";
    case "retried":
      return "warning";
    case "failed":
      return "destructive";
    default:
      return "outline";
  }
}

function checkStateVariant(state: CheckJobState): BadgeVariant {
  switch (state) {
    case "inFlight":
      return "default";
    case "idle":
      return "secondary";
    case "stalled":
      return "warning";
    case "crashLooping":
      return "destructive";
    default:
      return "outline";
  }
}

function formatPeriod(seconds: number): string {
  if (seconds <= 0) return "—";
  if (seconds % 3600 === 0) return `${seconds / 3600}h`;
  if (seconds % 60 === 0) return `${seconds / 60}m`;
  return `${seconds}s`;
}

function JobsIndexPage() {
  const { t } = useTranslation("jobs");
  const { org } = Route.useParams();
  const { user } = useAuth();
  const { tab, allOrgs: allOrgsSearch, status, type } = Route.useSearch();
  const navigate = useNavigate();

  const allOrgs = allOrgsSearch ?? false;
  const statusFilter = status ?? "all";
  const typeFilter = type ?? "all";

  // The tab is the page's core navigation element, so switching it pushes a
  // history entry (browser back/forward moves between tabs, deep links work).
  const setTab = (value: JobsTab) =>
    void navigate({ to: ".", search: (prev) => ({ ...prev, tab: value }) });
  // Scope and filters are refinements, so they replace the current entry.
  const setAllOrgs = (value: boolean) =>
    void navigate({
      to: ".",
      search: (prev) => ({ ...prev, allOrgs: value ? true : undefined }),
      replace: true,
    });
  const setStatusFilter = (value: string) =>
    void navigate({
      to: ".",
      search: (prev) => ({ ...prev, status: value === "all" ? undefined : value }),
      replace: true,
    });
  const setTypeFilter = (value: string) =>
    void navigate({
      to: ".",
      search: (prev) => ({ ...prev, type: value === "all" ? undefined : value }),
      replace: true,
    });

  const scope = useMemo(() => ({ allOrgs }), [allOrgs]);

  const stats = useJobsStats(org, scope);
  const active =
    (stats.data?.jobs.pending ?? 0) +
      (stats.data?.jobs.running ?? 0) +
      (stats.data?.checks.inFlight ?? 0) +
      (stats.data?.checks.dueNow ?? 0) >
    0;

  const backgroundJobs = useBackgroundJobs(org, {
    allOrgs,
    status: statusFilter === "all" ? undefined : statusFilter,
    type: typeFilter === "all" ? undefined : typeFilter,
    limit: PAGE_SIZE,
    active,
  });

  const checkSchedule = useCheckSchedule(org, {
    allOrgs,
    limit: PAGE_SIZE,
    active,
  });

  const refresh = () => {
    void stats.refetch();
    void backgroundJobs.refetch();
    void checkSchedule.refetch();
  };

  return (
    <div className="space-y-4">
      <PageHeader
        icon={Workflow}
        title={t("title")}
        description={t("subtitle")}
        className="flex-wrap"
        actions={
          <div className="flex items-center gap-2">
            {user?.isSuperAdmin && (
              <SegmentedControl
                value={allOrgs ? "all" : "this"}
                onValueChange={(next) => setAllOrgs(next === "all")}
                options={[
                  {
                    value: "this",
                    label: t("scope.thisOrg"),
                    testId: "scope-this-org",
                  },
                  {
                    value: "all",
                    label: t("scope.allOrgs"),
                    testId: "scope-all-orgs",
                  },
                ]}
              />
            )}
            <Button
              variant="outline"
              onClick={refresh}
              aria-label={t("refresh")}
            >
              <RefreshCw
                className={`h-4 w-4 sm:mr-2 ${
                  stats.isRefetching ? "animate-spin" : ""
                }`}
              />
              <span className="hidden sm:inline">{t("refresh")}</span>
            </Button>
          </div>
        }
      />

      <ActivityOverview stats={stats.data} loading={stats.isLoading} />

      <Tabs value={tab} onValueChange={(v) => setTab(v as "jobs" | "schedule")}>
        <TabsList>
          <TabsTrigger value="jobs">{t("tabs.backgroundJobs")}</TabsTrigger>
          <TabsTrigger value="schedule">{t("tabs.checkSchedule")}</TabsTrigger>
        </TabsList>

        <TabsContent value="jobs">
          <Card>
            <CardHeader>
              <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
                <CardTitle className="flex items-center gap-2">
                  <Workflow className="h-5 w-5" />
                  {t("tabs.backgroundJobs")}
                </CardTitle>
                <div className="flex flex-wrap gap-2">
                  <Select value={statusFilter} onValueChange={setStatusFilter}>
                    <SelectTrigger className="w-40" aria-label={t("filters.status")}>
                      <SelectValue placeholder={t("filters.status")} />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="all">{t("filters.allStatuses")}</SelectItem>
                      {JOB_STATUSES.map((s) => (
                        <SelectItem key={s} value={s}>
                          {t(`status.${s}`)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <Select value={typeFilter} onValueChange={setTypeFilter}>
                    <SelectTrigger className="w-44" aria-label={t("filters.type")}>
                      <SelectValue placeholder={t("filters.type")} />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="all">{t("filters.allTypes")}</SelectItem>
                      {JOB_TYPES.map((ty) => (
                        <SelectItem key={ty} value={ty}>
                          {ty}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
            </CardHeader>
            <CardContent>
              <BackgroundJobsTable
                org={org}
                allOrgs={allOrgs}
                jobs={backgroundJobs.data}
                loading={backgroundJobs.isLoading}
              />
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="schedule">
          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Activity className="h-5 w-5" />
                {t("tabs.checkSchedule")}
              </CardTitle>
            </CardHeader>
            <CardContent>
              <CheckScheduleTable
                org={org}
                allOrgs={allOrgs}
                rows={checkSchedule.data}
                loading={checkSchedule.isLoading}
              />
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  );
}

function ActivityOverview({
  stats,
  loading,
}: {
  stats: ReturnType<typeof useJobsStats>["data"];
  loading: boolean;
}) {
  const { t } = useTranslation("jobs");

  if (loading) {
    return (
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
        {Array.from({ length: 6 }).map((_, i) => (
          <Skeleton key={i} className="h-16 w-full" />
        ))}
      </div>
    );
  }

  const tiles: { label: string; value: number; tone: StatTileTone }[] = [
    { label: t("overview.pending"), value: stats?.jobs.pending ?? 0, tone: "info" },
    { label: t("overview.running"), value: stats?.jobs.running ?? 0, tone: "info" },
    {
      label: t("overview.failed24h"),
      value: stats?.jobs.failed24h ?? 0,
      tone: (stats?.jobs.failed24h ?? 0) > 0 ? "destructive" : "default",
    },
    { label: t("overview.scheduled"), value: stats?.checks.total ?? 0, tone: "default" },
    { label: t("overview.dueNow"), value: stats?.checks.dueNow ?? 0, tone: "default" },
    {
      label: t("overview.inFlight"),
      value: stats?.checks.inFlight ?? 0,
      tone: "success",
    },
    {
      label: t("overview.stalled"),
      value: stats?.checks.stalled ?? 0,
      tone: (stats?.checks.stalled ?? 0) > 0 ? "warning" : "default",
    },
    {
      label: t("overview.crashLooping"),
      value: stats?.checks.crashLooping ?? 0,
      tone: (stats?.checks.crashLooping ?? 0) > 0 ? "destructive" : "default",
    },
  ];

  return (
    <div
      className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-8"
      data-testid="jobs-overview"
    >
      {tiles.map((tile) => (
        <StatTile key={tile.label} label={tile.label} value={tile.value} tone={tile.tone} />
      ))}
    </div>
  );
}

function BackgroundJobsTable({
  org,
  allOrgs,
  jobs,
  loading,
}: {
  org: string;
  allOrgs: boolean;
  jobs: BackgroundJob[] | undefined;
  loading: boolean;
}) {
  const { t } = useTranslation("jobs");

  if (loading) {
    return (
      <div className="space-y-2">
        {[1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-10 w-full" />
        ))}
      </div>
    );
  }

  if (!jobs || jobs.length === 0) {
    return <p className="text-sm text-muted-foreground">{t("empty.backgroundJobs")}</p>;
  }

  return (
    <div className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t("columns.type")}</TableHead>
            <TableHead>{t("columns.status")}</TableHead>
            {allOrgs && <TableHead>{t("columns.org")}</TableHead>}
            <TableHead>{t("columns.scheduled")}</TableHead>
            <TableHead>{t("columns.retries")}</TableHead>
            <TableHead>{t("columns.updated")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {jobs.map((job) => (
            <TableRow key={job.uid} className="cursor-pointer">
              <TableCell>
                <Link
                  to="/orgs/$org/jobs/$jobUid"
                  params={{ org, jobUid: job.uid }}
                  search={allOrgs ? { allOrgs: true } : {}}
                  className="hover:underline"
                >
                  <Badge variant="outline">{job.type}</Badge>
                </Link>
              </TableCell>
              <TableCell>
                <Badge variant={jobStatusVariant(job.status)}>
                  {t(`status.${job.status}`, job.status)}
                </Badge>
              </TableCell>
              {allOrgs && (
                <TableCell className="text-xs text-muted-foreground">
                  {job.organizationUid ?? (
                    <Badge variant="outline">{t("labels.system")}</Badge>
                  )}
                </TableCell>
              )}
              <TableCell className="text-xs text-muted-foreground">
                <TimeAgoOrDash date={job.scheduledAt} data-testid="job-scheduled-at" />
              </TableCell>
              <TableCell className="text-xs text-muted-foreground tabular-nums">
                {job.retryCount}
              </TableCell>
              <TableCell className="text-xs text-muted-foreground">
                <TimeAgoOrDash date={job.updatedAt} data-testid="job-updated-at" />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}

function CheckScheduleTable({
  org,
  allOrgs,
  rows,
  loading,
}: {
  org: string;
  allOrgs: boolean;
  rows: CheckScheduleJob[] | undefined;
  loading: boolean;
}) {
  const { t } = useTranslation("jobs");
  // Job rows carry the raw region slug; show the friendly "{emoji} {name}"
  // label from the region definitions (raw-slug fallback).
  const { data: regionsData } = useRegions(org);

  if (loading) {
    return (
      <div className="space-y-2">
        {[1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-10 w-full" />
        ))}
      </div>
    );
  }

  if (!rows || rows.length === 0) {
    return <p className="text-sm text-muted-foreground">{t("empty.checkSchedule")}</p>;
  }

  return (
    <div className="overflow-x-auto">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t("columns.check")}</TableHead>
            <TableHead>{t("columns.region")}</TableHead>
            {allOrgs && <TableHead>{t("columns.org")}</TableHead>}
            <TableHead>{t("columns.state")}</TableHead>
            <TableHead>{t("columns.period")}</TableHead>
            <TableHead>{t("columns.nextRun")}</TableHead>
            <TableHead>{t("columns.attempts")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row) => (
            <TableRow key={row.uid}>
              <TableCell>
                <Link
                  to="/orgs/$org/jobs/check/$checkJobUid"
                  params={{ org, checkJobUid: row.uid }}
                  search={allOrgs ? { allOrgs: true } : {}}
                  className="font-medium hover:underline"
                >
                  {row.checkName ?? row.checkUid}
                </Link>
              </TableCell>
              <TableCell className="text-xs text-muted-foreground">
                {row.region
                  ? regionDisplayLabel(regionsData?.regions, row.region)
                  : t("labels.none")}
              </TableCell>
              {allOrgs && (
                <TableCell className="text-xs text-muted-foreground">
                  {row.organizationUid}
                </TableCell>
              )}
              <TableCell>
                <Badge variant={checkStateVariant(row.state)}>
                  {t(`state.${row.state}`, row.state)}
                </Badge>
              </TableCell>
              <TableCell className="text-xs text-muted-foreground">
                {formatPeriod(row.periodSeconds)}
              </TableCell>
              <TableCell className="text-xs text-muted-foreground">
                <TimeAgoOrDash date={row.scheduledAt} data-testid="check-schedule-next-run" />
              </TableCell>
              <TableCell className="text-xs text-muted-foreground tabular-nums">
                {row.leaseStarts}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
