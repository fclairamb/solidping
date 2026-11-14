import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { ArrowDown, BarChart3 } from "lucide-react";
import { useIncidentInsights, type InsightsPeriod } from "@/api/hooks";
import { formatDuration } from "@/components/shared/relative-time";
import { QueryErrorView } from "@/components/shared/error-views";
import { PageHeader } from "@/components/shared/page-header";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { TimeAgo } from "@/components/ui/time-ago";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

const PERIODS: InsightsPeriod[] = ["7d", "30d", "90d"];

interface InsightsSearch {
  period: InsightsPeriod;
}

export const Route = createFileRoute("/orgs/$org/incidents/insights")({
  validateSearch: (search: Record<string, unknown>): InsightsSearch => ({
    period: PERIODS.includes(search.period as InsightsPeriod)
      ? (search.period as InsightsPeriod)
      : "30d",
  }),
  component: IncidentInsightsPage,
});

type SortKey = "incidentCount" | "downtimeSeconds" | "relapseCount";

const seconds = (value: number | null): string | null =>
  value === null ? null : formatDuration(value * 1000);

function IncidentInsightsPage() {
  const { t } = useTranslation("incidents");
  const { org } = Route.useParams();
  const { period } = Route.useSearch();
  const navigate = useNavigate();
  const [sortKey, setSortKey] = useState<SortKey>("incidentCount");

  const { data, isLoading, error, refetch } = useIncidentInsights(org, period);

  const rows = useMemo(() => {
    const list = [...(data?.checks ?? [])];
    // Stable: the server already ordered by incidents then downtime.
    if (sortKey !== "incidentCount") {
      list.sort((a, b) => b[sortKey] - a[sortKey]);
    }
    return list;
  }, [data, sortKey]);

  if (error) {
    return <QueryErrorView error={error} org={org} onRetry={() => refetch()} />;
  }

  const summary = data?.summary;

  return (
    <div className="space-y-6" data-testid="incident-insights-page">
      <PageHeader
        icon={BarChart3}
        title={t("insights.title")}
        description={t("insights.subtitle")}
        docsHref="/docs/features/incidents"
        actions={
          <div
            className="flex flex-wrap items-center gap-2"
            data-testid="insights-period-selector"
          >
            {PERIODS.map((p) => (
              <Button
                key={p}
                size="sm"
                variant={p === period ? "default" : "outline"}
                data-testid={`insights-period-${p}`}
                aria-pressed={p === period}
                onClick={() =>
                  navigate({
                    to: "/orgs/$org/incidents/insights",
                    params: { org },
                    search: { period: p },
                  })
                }
              >
                {t(`insights.days${p.slice(0, -1)}`)}
              </Button>
            ))}
          </div>
        }
      />

      {isLoading || !data || !summary ? (
        <Skeleton className="h-48 w-full" />
      ) : summary.incidentCount === 0 ? (
        <Card data-testid="insights-empty">
          <CardContent className="py-10 text-center text-muted-foreground">
            {t("insights.empty")}
          </CardContent>
        </Card>
      ) : (
        <>
          {data.truncated && (
            <div
              className="rounded-md border border-amber-500/40 bg-amber-500/10 p-3 text-sm"
              data-testid="insights-truncated"
            >
              {t("insights.truncated")}
            </div>
          )}

          <Card data-testid="insights-top-checks">
            <CardHeader>
              <CardTitle>{t("insights.topChecks")}</CardTitle>
            </CardHeader>
            <CardContent className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("insights.colCheck")}</TableHead>
                    <SortHead
                      label={t("insights.colIncidents")}
                      active={sortKey === "incidentCount"}
                      onClick={() => setSortKey("incidentCount")}
                      testId="insights-sort-incidents"
                    />
                    <SortHead
                      label={t("insights.colDowntime")}
                      active={sortKey === "downtimeSeconds"}
                      onClick={() => setSortKey("downtimeSeconds")}
                      testId="insights-sort-downtime"
                    />
                    <SortHead
                      label={t("insights.colRelapses")}
                      active={sortKey === "relapseCount"}
                      onClick={() => setSortKey("relapseCount")}
                      testId="insights-sort-relapses"
                    />
                    <TableHead>{t("insights.colLast")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((row) => (
                    <TableRow
                      key={row.checkUid}
                      data-testid="insights-check-row"
                    >
                      <TableCell className="font-medium">
                        <Link
                          to="/orgs/$org/checks/$checkUid"
                          params={{ org, checkUid: row.checkUid }}
                          className="hover:underline"
                          data-testid="insights-check-link"
                        >
                          {row.checkName || row.checkSlug || row.checkUid}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <Link
                          to="/orgs/$org/incidents"
                          params={{ org }}
                          search={{
                            state: "all",
                            showSuppressed: true,
                            checkUid: row.checkUid,
                          }}
                          className="hover:underline"
                          data-testid="insights-incident-count"
                        >
                          {row.incidentCount}
                        </Link>
                      </TableCell>
                      <TableCell>
                        {formatDuration(row.downtimeSeconds * 1000)}
                      </TableCell>
                      <TableCell>{row.relapseCount}</TableCell>
                      <TableCell>
                        {row.lastIncidentAt ? (
                          <TimeAgo date={row.lastIncidentAt} />
                        ) : (
                          "-"
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>

          <h2 className="text-lg font-semibold">
            {t("insights.responseTimes")}
          </h2>
          <div className="grid gap-4 md:grid-cols-3">
            <StatCard
              testId="insights-card-ack"
              title={t("insights.tta")}
              median={seconds(summary.timeToAcknowledge.medianSeconds)}
              p90={seconds(summary.timeToAcknowledge.p90Seconds)}
              empty={t("insights.noAck")}
              footer={t("insights.ackedOf", {
                acked: summary.acknowledgedCount,
                total: summary.pagedCount,
              })}
            />
            <StatCard
              testId="insights-card-resolve"
              title={t("insights.ttr")}
              median={seconds(summary.timeToResolve.medianSeconds)}
              p90={seconds(summary.timeToResolve.p90Seconds)}
              empty={t("insights.noResolve")}
            />
            <Card data-testid="insights-card-resolution">
              <CardHeader>
                <CardTitle className="text-base">
                  {t("insights.resolution")}
                </CardTitle>
              </CardHeader>
              <CardContent className="space-y-2 text-sm">
                {(["auto", "manual", "other"] as const).map((kind) => {
                  const stat = summary.byResolution[kind];
                  const median = seconds(stat.medianSeconds);
                  return (
                    <div
                      key={kind}
                      className="flex flex-wrap justify-between gap-2"
                      data-testid={`insights-resolution-${kind}`}
                    >
                      <span>{t(`insights.${kind}`)}</span>
                      <span className="text-muted-foreground">
                        {t("insights.n", { n: stat.count })}
                        {kind !== "other" && median
                          ? ` · ${t("insights.medianValue", { value: median })}`
                          : ""}
                      </span>
                    </div>
                  );
                })}
              </CardContent>
            </Card>
          </div>
        </>
      )}
    </div>
  );
}

function SortHead({
  label,
  active,
  onClick,
  testId,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
  testId: string;
}) {
  return (
    <TableHead>
      <button
        type="button"
        className="inline-flex items-center gap-1 whitespace-nowrap font-medium"
        onClick={onClick}
        data-testid={testId}
      >
        {label}
        {active && <ArrowDown className="h-3 w-3" />}
      </button>
    </TableHead>
  );
}

function StatCard({
  testId,
  title,
  median,
  p90,
  empty,
  footer,
}: {
  testId: string;
  title: string;
  median: string | null;
  p90: string | null;
  empty: string;
  footer?: string;
}) {
  const { t } = useTranslation("incidents");

  return (
    <Card data-testid={testId}>
      <CardHeader>
        <CardTitle className="text-base">{title}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-1">
        {median === null ? (
          <>
            <div className="text-2xl font-semibold">-</div>
            <p className="text-sm text-muted-foreground">{empty}</p>
          </>
        ) : (
          <>
            <div className="text-2xl font-semibold">{median}</div>
            <p className="text-sm text-muted-foreground">
              {t("insights.median")} · {t("insights.p90")} {p90}
            </p>
          </>
        )}
        {footer && <p className="text-sm text-muted-foreground">{footer}</p>}
      </CardContent>
    </Card>
  );
}
