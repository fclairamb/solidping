import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { subDays, startOfDay, startOfMinute } from "date-fns";
import { useAllResults, useIncidents } from "@/api/hooks";
import type { IncidentDetail, OrgResult } from "@/api/hooks";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";

type PeriodId = "today" | "last7" | "last30" | "last365";

export type AvailabilityChartPeriod = "day" | "week" | "month";

interface AvailabilityTableProps {
  org: string;
  checkUid: string;
  refetchInterval?: number;
  onPeriodSelect?: (period: AvailabilityChartPeriod) => void;
}

interface PeriodConfig {
  id: PeriodId;
  labelKey: "today" | "last7" | "last30" | "last365";
  shortLabel: string;
  getStart: () => Date;
  durationMs: number;
}

const PERIODS: PeriodConfig[] = [
  {
    id: "today",
    labelKey: "today",
    shortLabel: "1d",
    getStart: () => startOfDay(new Date()),
    durationMs: Date.now() - startOfDay(new Date()).getTime(),
  },
  {
    id: "last7",
    labelKey: "last7",
    shortLabel: "7d",
    getStart: () => subDays(new Date(), 7),
    durationMs: 7 * 24 * 60 * 60 * 1000,
  },
  {
    id: "last30",
    labelKey: "last30",
    shortLabel: "30d",
    getStart: () => subDays(new Date(), 30),
    durationMs: 30 * 24 * 60 * 60 * 1000,
  },
  {
    id: "last365",
    labelKey: "last365",
    shortLabel: "1y",
    getStart: () => subDays(new Date(), 365),
    durationMs: 365 * 24 * 60 * 60 * 1000,
  },
];

function formatAvailability(pct: number): string {
  if (pct >= 100) return "100%";
  if (pct >= 99) return `${pct.toFixed(2)}%`;
  return `${pct.toFixed(1)}%`;
}

const ROW_TO_GRAPH: Record<PeriodId, AvailabilityChartPeriod | null> = {
  today: "day",
  last7: "week",
  last30: "month",
  last365: null,
};

function formatDuration(ms: number): string {
  if (ms <= 0) return "0s";
  const seconds = Math.floor(ms / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);

  if (days > 0) return `${days}d ${hours % 24}h`;
  if (hours > 0) return `${hours}h ${minutes % 60}m`;
  if (minutes > 0) return `${minutes}m ${seconds % 60}s`;
  return `${seconds}s`;
}

function computeIncidentStats(
  incidents: IncidentDetail[],
  start: Date,
  now: Date
) {
  const filtered = incidents.filter((inc) => {
    if (!inc.startedAt) return false;
    const startedAt = new Date(inc.startedAt);
    return startedAt >= start && startedAt <= now;
  });

  if (filtered.length === 0) {
    return { count: 0, longest: 0, average: 0 };
  }

  let totalDuration = 0;
  let longest = 0;

  for (const inc of filtered) {
    const s = new Date(inc.startedAt!).getTime();
    const e = inc.resolvedAt ? new Date(inc.resolvedAt).getTime() : now.getTime();
    const dur = e - s;
    totalDuration += dur;
    if (dur > longest) longest = dur;
  }

  return {
    count: filtered.length,
    longest,
    average: Math.round(totalDuration / filtered.length),
  };
}

export function AvailabilityTable({ org, checkUid, refetchInterval, onPeriodSelect }: AvailabilityTableProps) {
  const { t } = useTranslation("checks");
  // Memoize timestamps to the current minute so query keys are stable across re-renders
  const yearAgo = useMemo(() => {
    const now = startOfMinute(new Date());
    return subDays(now, 365).toISOString();
  }, []);

  // Include raw as a co-tier so the current open bucket (never rolled up by
  // the aggregator until it closes) contributes to availability immediately.
  // Raw retention is 24 h by default, so the extra payload is bounded.
  const tableRefetchInterval = Math.max(refetchInterval ?? 60_000, 60_000);

  const { data: allResults, isLoading: loadingResults } = useAllResults(org, {
    checkUid,
    periodStartAfter: yearAgo,
    periodType: "raw,hour,day,month",
    with: "availabilityPct,totalChecks,successfulChecks,status",
    size: 1000,
    refetchInterval: tableRefetchInterval,
  });

  // Bucket all results (raw + aggregated) by the period windows we need.
  const { todayRows, last7Rows, last30Rows, last365Rows } = useMemo(() => {
    const data = allResults?.data ?? [];
    const now = new Date();
    const todayStart = startOfDay(now).getTime();
    const day7 = subDays(now, 7).getTime();
    const day30 = subDays(now, 30).getTime();
    const day365 = subDays(now, 365).getTime();
    return {
      todayRows:   data.filter((r) => r.periodStart && new Date(r.periodStart).getTime() >= todayStart),
      last7Rows:   data.filter((r) => r.periodStart && new Date(r.periodStart).getTime() >= day7),
      last30Rows:  data.filter((r) => r.periodStart && new Date(r.periodStart).getTime() >= day30),
      last365Rows: data.filter((r) => r.periodStart && new Date(r.periodStart).getTime() >= day365),
    };
  }, [allResults]);

  const { data: incidents, isLoading: loadingIncidents } = useIncidents(org, {
    checkUid,
    size: 100,
    refetchInterval: tableRefetchInterval,
  });

  const isLoading = loadingResults || loadingIncidents;

  const rows = useMemo(() => {
    const now = new Date();

    // Compute availability from a mix of raw and aggregated rows. Mirrors
    // calculateAvailability() in server/internal/handlers/badges/service.go.
    // Aggregated rows are weighted by their actual sample count so a bucket
    // with many checks isn't treated as a single "vote".
    function computeAvailability(data: OrgResult[] | undefined): number | null {
      if (!data?.length) return null;
      let successful = 0;
      let total = 0;
      for (const r of data) {
        if (r.periodType === "raw") {
          if (r.status === "created" || r.status === "running") continue;
          total += 1;
          if (r.status === "up") successful += 1;
        } else if (r.totalChecks != null && r.successfulChecks != null) {
          total += r.totalChecks;
          successful += r.successfulChecks;
        } else if (r.availabilityPct != null) {
          total += 1;
          successful += r.availabilityPct / 100;
        }
      }
      return total === 0 ? null : (successful / total) * 100;
    }

    const incidentData = incidents?.data || [];

    return PERIODS.map((period) => {
      let availability: number | null;
      switch (period.id) {
        case "today":
          availability = computeAvailability(todayRows);
          break;
        case "last7":
          availability = computeAvailability(last7Rows);
          break;
        case "last30":
          availability = computeAvailability(last30Rows);
          break;
        case "last365":
          availability = computeAvailability(last365Rows);
          break;
        default:
          availability = null;
      }

      const incStats = computeIncidentStats(incidentData, period.getStart(), now);

      const downtime =
        availability != null
          ? (1 - availability / 100) * period.durationMs
          : null;

      return {
        id: period.id,
        labelKey: period.labelKey,
        shortLabel: period.shortLabel,
        availability,
        downtime,
        incidents: incStats.count,
        longest: incStats.longest,
        average: incStats.average,
      };
    });
  }, [todayRows, last7Rows, last30Rows, last365Rows, incidents]);

  if (isLoading) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>{t("detail.availability.title")}</CardTitle>
        </CardHeader>
        <CardContent>
          <Skeleton className="h-48 w-full" />
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("detail.availability.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("detail.availability.timePeriod")}</TableHead>
              <TableHead>{t("detail.availability.availability")}</TableHead>
              <TableHead>{t("detail.availability.downtime")}</TableHead>
              <TableHead>{t("detail.availability.incidents")}</TableHead>
              <TableHead>{t("detail.availability.longestIncident")}</TableHead>
              <TableHead>{t("detail.availability.avgIncident")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((row) => {
              const graphPeriod = ROW_TO_GRAPH[row.id];
              const clickable = graphPeriod != null && onPeriodSelect != null;
              return (
                <TableRow
                  key={row.id}
                  className={clickable ? "cursor-pointer hover:bg-muted/50" : undefined}
                  onClick={
                    clickable ? () => onPeriodSelect!(graphPeriod!) : undefined
                  }
                >
                  <TableCell className="font-medium">
                    <span className="sm:hidden">{row.shortLabel}</span>
                    <span className="hidden sm:inline">{t(`detail.availability.${row.labelKey}`)}</span>
                  </TableCell>
                  <TableCell>
                    {row.availability != null
                      ? formatAvailability(row.availability)
                      : "-"}
                  </TableCell>
                  <TableCell>
                    {row.downtime != null ? formatDuration(row.downtime) : "-"}
                  </TableCell>
                  <TableCell>{row.incidents}</TableCell>
                  <TableCell>
                    {row.incidents > 0 ? formatDuration(row.longest) : t("detail.availability.none")}
                  </TableCell>
                  <TableCell>
                    {row.incidents > 0 ? formatDuration(row.average) : t("detail.availability.none")}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}
