import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { useCheckAvailability } from "@/api/hooks";
import type { CheckAvailabilityPeriod } from "@/api/hooks";
import { mapAvailabilityRow, selectAvailabilityRows } from "@/lib/availability-rows";
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

interface AvailabilityTableProps {
  org: string;
  checkUid: string;
  refetchInterval?: number;
}

// The period tokens we ask the server for, in display order (shortest window
// first), with their label key. The server measures each window; the client
// does no availability math.
const PERIOD_ROWS: {
  token: string;
  labelKey: "last1h" | "last24h" | "last30" | "last90" | "last365";
  shortLabel: string;
}[] = [
  { token: "1h", labelKey: "last1h", shortLabel: "1h" },
  { token: "24h", labelKey: "last24h", shortLabel: "24h" },
  { token: "30d", labelKey: "last30", shortLabel: "30d" },
  { token: "90d", labelKey: "last90", shortLabel: "90d" },
  { token: "365d", labelKey: "last365", shortLabel: "1y" },
];

const PERIODS_PARAM = PERIOD_ROWS.map((p) => p.token).join(",");

export function AvailabilityTable({
  org,
  checkUid,
  refetchInterval,
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
            {selectAvailabilityRows(
              PERIOD_ROWS.map((row) => byToken.get(row.token)),
            ).map(({ index, collapsed }) => {
              const row = PERIOD_ROWS[index];
              const view = mapAvailabilityRow(byToken.get(row.token));
              // A collapsed row stands for the whole monitored history (all the
              // longer windows measure the same span), so it gets its own label
              // and the short form shows the actual days of data, not "1y".
              const label = collapsed
                ? t("detail.availability.sinceCreation")
                : t(`detail.availability.${row.labelKey}`);
              const shortLabel = collapsed
                ? `${view.monitoredDays ?? 1}d`
                : row.shortLabel;

              return (
                <TableRow key={row.token}>
                  <TableCell className="font-medium">
                    <span className="sm:hidden">{shortLabel}</span>
                    <span className="hidden sm:inline">{label}</span>
                    {collapsed && view.monitoredDays != null && (
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
