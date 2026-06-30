import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { useCheckAvailability } from "@/api/hooks";
import type { CheckAvailabilityPeriod } from "@/api/hooks";
import { mapAvailabilityRow } from "@/lib/availability-rows";
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

export type AvailabilityChartPeriod = "day" | "week" | "month";

interface AvailabilityTableProps {
  org: string;
  checkUid: string;
  refetchInterval?: number;
  onPeriodSelect?: (period: AvailabilityChartPeriod) => void;
}

// The period tokens we ask the server for, in display order, with their label
// key and the chart period each row drills into. The server measures each
// window; the client does no availability math.
const PERIOD_ROWS: {
  token: string;
  labelKey: "today" | "last7" | "last30" | "last365";
  shortLabel: string;
  graph: AvailabilityChartPeriod | null;
}[] = [
  { token: "today", labelKey: "today", shortLabel: "1d", graph: "day" },
  { token: "7d", labelKey: "last7", shortLabel: "7d", graph: "week" },
  { token: "30d", labelKey: "last30", shortLabel: "30d", graph: "month" },
  { token: "365d", labelKey: "last365", shortLabel: "1y", graph: null },
];

const PERIODS_PARAM = PERIOD_ROWS.map((p) => p.token).join(",");

export function AvailabilityTable({
  org,
  checkUid,
  refetchInterval,
  onPeriodSelect,
}: AvailabilityTableProps) {
  const { t } = useTranslation("checks");

  const tableRefetchInterval = Math.max(refetchInterval ?? 60_000, 60_000);

  const { data, isLoading } = useCheckAvailability(org, checkUid, PERIODS_PARAM, {
    refetchInterval: tableRefetchInterval,
  });

  // Index the server's periods by token so each display row reads straight from
  // the matching measurement (no recomputation client-side).
  const byToken = useMemo(() => {
    const map = new Map<string, CheckAvailabilityPeriod>();
    for (const p of data?.data ?? []) map.set(p.period, p);
    return map;
  }, [data]);

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
            {PERIOD_ROWS.map((row) => {
              const view = mapAvailabilityRow(byToken.get(row.token));
              const clickable = row.graph != null && onPeriodSelect != null;

              return (
                <TableRow
                  key={row.token}
                  className={clickable ? "cursor-pointer hover:bg-muted/50" : undefined}
                  onClick={
                    clickable ? () => onPeriodSelect!(row.graph!) : undefined
                  }
                >
                  <TableCell className="font-medium">
                    <span className="sm:hidden">{row.shortLabel}</span>
                    <span className="hidden sm:inline">
                      {t(`detail.availability.${row.labelKey}`)}
                    </span>
                    {view.monitoredDays != null && (
                      <span className="block text-xs text-muted-foreground">
                        {t("detail.availability.monitored", { days: view.monitoredDays })}
                      </span>
                    )}
                  </TableCell>
                  <TableCell>{view.availabilityText}</TableCell>
                  <TableCell>{view.downtimeText}</TableCell>
                  <TableCell>{view.incidentCount}</TableCell>
                  <TableCell>
                    {view.longestText ?? t("detail.availability.none")}
                  </TableCell>
                  <TableCell>
                    {view.averageText ?? t("detail.availability.none")}
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
