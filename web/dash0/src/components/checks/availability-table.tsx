import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { subDays, startOfDay, startOfMinute } from "date-fns";
import { useAllResults, useIncidents } from "@/api/hooks";
import type { IncidentDetail } from "@/api/hooks";
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

  // Aggregated tiers only — the table never displays raw rows. ~70 buckets
  // for a year of daily + monthly data fits well under the size cap. Bump
  // the refetch floor to 60s; annual availability doesn't move that fast.
  const tableRefetchInterval = Math.max(refetchInterval ?? 60_000, 60_000);

  const { data: allResults, isLoading: loadingResults } = useAllResults(org, {
    checkUid,
    periodStartAfter: yearAgo,
    periodType: "hour,day,month",
    with: "availabilityPct",
    size: 1000,
    refetchInterval: tableRefetchInterval,
  });

  // Filter results by periodType client-side
  const { hourlyResults, dailyResults, monthlyResults } = useMemo(() => {
    const data = allResults?.data || [];
    const todayStart = startOfDay(new Date());
    const thirtyDaysAgo = subDays(new Date(), 30);
    return {
      hourlyResults: data.filter((r) => r.periodType === "hour" && r.periodStart && new Date(r.periodStart) >= todayStart),
      dailyResults: data.filter((r) => r.periodType === "day" && r.periodStart && new Date(r.periodStart) >= thirtyDaysAgo),
      monthlyResults: data.filter((r) => r.periodType === "month"),
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

    function avgAvailability(
      data: { availabilityPct?: number }[] | undefined
    ): number | null {
      if (!data?.length) return null;
      const valid = data.filter(
        (r) => r.availabilityPct != null
      ) as { availabilityPct: number }[];
      if (valid.length === 0) return null;
      return valid.reduce((sum, r) => sum + r.availabilityPct, 0) / valid.length;
    }

    // Filter daily results for 7-day window
    const sevenDaysStart = subDays(now, 7);
    const daily7d = dailyResults.filter(
      (r) => r.periodStart && new Date(r.periodStart) >= sevenDaysStart
    );

    const incidentData = incidents?.data || [];

    return PERIODS.map((period) => {
      let availability: number | null;
      switch (period.id) {
        case "today":
          availability = avgAvailability(hourlyResults);
          break;
        case "last7":
          availability = avgAvailability(daily7d);
          break;
        case "last30":
          availability = avgAvailability(dailyResults);
          break;
        case "last365":
          availability = avgAvailability(monthlyResults);
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
  }, [hourlyResults, dailyResults, monthlyResults, incidents]);

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
