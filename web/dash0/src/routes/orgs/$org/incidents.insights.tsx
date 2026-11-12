import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { BarChart3 } from "lucide-react";
import { useIncidentInsights, type IncidentInsights } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { TimeAgo } from "@/components/ui/time-ago";
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
import { formatDurationSeconds } from "@/lib/availability-rows";

const PERIODS = ["7d", "30d", "90d"] as const;
type Period = (typeof PERIODS)[number];

type SortKey = "incidentCount" | "downtimeSeconds" | "relapseCount";

interface InsightsSearch {
  period: Period;
}

export const Route = createFileRoute("/orgs/$org/incidents/insights")({
  validateSearch: (search: Record<string, unknown>): InsightsSearch => ({
    period: PERIODS.includes(search.period as Period)
      ? (search.period as Period)
      : "30d",
  }),
  component: IncidentInsightsPage,
});

const fmt = (seconds: number | null) =>
  seconds === null ? "-" : formatDurationSeconds(seconds);

function StatCard({
  testId,
  title,
  children,
}: {
  testId: string;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <div className="rounded-lg border bg-card p-4" data-testid={testId}>
      <h3 className="text-sm font-medium text-muted-foreground">{title}</h3>
      <div className="mt-2 space-y-1">{children}</div>
    </div>
  );
}

function Figure({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-4">
      <span className="text-sm text-muted-foreground">{label}</span>
      <span className="text-xl font-semibold tabular-nums">{value}</span>
    </div>
  );
}

function IncidentInsightsPage() {
  const { t } = useTranslation("incidents");
  const { org } = Route.useParams();
  const { period } = Route.useSearch();
  const navigate = useNavigate();
  const { data, error, isLoading, refetch } = useIncidentInsights(org, period);
  const [sort, setSort] = useState<SortKey>("incidentCount");

  return (
    <div className="space-y-6">
      <PageHeader
        icon={BarChart3}
        title={t("insights.title")}
        description={t("insights.subtitle")}
        className="flex-wrap"
        actions={
          <Button variant="outline" asChild data-testid="insights-back-button">
            <Link
              to="/orgs/$org/incidents"
              params={{ org }}
              search={{
                state: "all" as const,
                showSuppressed: undefined,
                checkUid: undefined,
              }}
              data-testid="insights-back-link"
            >
              {t("insights.back")}
            </Link>
          </Button>
        }
      />

      <Select
        value={period}
        onValueChange={(value) =>
          navigate({
            to: ".",
            search: { period: value as Period },
            replace: true,
          })
        }
      >
        <SelectTrigger
          className="w-[180px]"
          data-testid="insights-period-select"
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {PERIODS.map((p) => (
            <SelectItem key={p} value={p} data-testid={`insights-period-${p}`}>
              {t(`insights.period.${p}`)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      {error ? (
        <QueryErrorView error={error} org={org} onRetry={() => refetch()} />
      ) : isLoading || !data ? (
        <Skeleton className="h-64 w-full" />
      ) : data.summary.incidentCount === 0 ? (
        <div
          className="rounded-lg border p-8 text-center text-muted-foreground"
          data-testid="insights-empty"
        >
          {t("insights.empty")}
        </div>
      ) : (
        <InsightsBody data={data} org={org} sort={sort} onSort={setSort} />
      )}
    </div>
  );
}

function InsightsBody({
  data,
  org,
  sort,
  onSort,
}: {
  data: IncidentInsights;
  org: string;
  sort: SortKey;
  onSort: (key: SortKey) => void;
}) {
  const { t } = useTranslation("incidents");
  const { summary } = data;
  const rows = [...data.checks].sort((a, b) => b[sort] - a[sort]);
  const sortHead = (key: SortKey, label: string) => (
    <TableHead className="text-right">
      <button
        type="button"
        className={sort === key ? "font-semibold text-foreground" : ""}
        onClick={() => onSort(key)}
        data-testid={`insights-sort-${key}`}
      >
        {label}
      </button>
    </TableHead>
  );

  return (
    <>
      {data.truncated && (
        <div
          className="rounded-lg border border-yellow-500/40 p-3 text-sm"
          data-testid="insights-truncated"
        >
          {t("insights.truncated")}
        </div>
      )}

      <section className="space-y-3">
        <h2 className="text-lg font-semibold">{t("insights.topChecks")}</h2>
        <div className="overflow-x-auto rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("insights.columns.check")}</TableHead>
                {sortHead("incidentCount", t("insights.columns.incidents"))}
                {sortHead("downtimeSeconds", t("insights.columns.downtime"))}
                {sortHead("relapseCount", t("insights.columns.relapses"))}
                <TableHead>{t("insights.columns.last")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row) => (
                <TableRow
                  key={row.checkUid}
                  data-testid="insights-check-row"
                  data-check-uid={row.checkUid}
                >
                  <TableCell>
                    <Link
                      to="/orgs/$org/checks/$checkUid"
                      params={{ org, checkUid: row.checkUid }}
                      className="font-medium hover:underline"
                      data-testid="insights-check-link"
                    >
                      {row.name || row.slug || row.checkUid}
                    </Link>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    <Link
                      to="/orgs/$org/incidents"
                      params={{ org }}
                      search={{
                        state: "all" as const,
                        showSuppressed: undefined,
                        checkUid: row.checkUid,
                      }}
                      className="hover:underline"
                      data-testid="insights-check-incidents-link"
                    >
                      {row.incidentCount}
                    </Link>
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {formatDurationSeconds(row.downtimeSeconds)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {row.relapseCount}
                  </TableCell>
                  <TableCell>
                    {row.lastIncidentAt && (
                      <TimeAgo date={row.lastIncidentAt} />
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      </section>

      <section className="space-y-3">
        <h2 className="text-lg font-semibold">{t("insights.responseTimes")}</h2>
        <div className="grid gap-4 md:grid-cols-3">
          <StatCard testId="insights-ack-card" title={t("insights.ack.title")}>
            {summary.timeToAcknowledge.sampleCount === 0 ? (
              <EmptyReason text={t("insights.ack.empty")} />
            ) : (
              <>
                <Figure
                  label={t("insights.ack.median")}
                  value={fmt(summary.timeToAcknowledge.medianSeconds)}
                />
                <Figure
                  label={t("insights.ack.p90")}
                  value={fmt(summary.timeToAcknowledge.p90Seconds)}
                />
              </>
            )}
            <p className="text-sm text-muted-foreground">
              {t("insights.ack.rate", {
                acked: summary.acknowledgedCount,
                paged: summary.pagedCount,
              })}
            </p>
          </StatCard>

          <StatCard
            testId="insights-resolve-card"
            title={t("insights.resolve.title")}
          >
            {summary.timeToResolve.sampleCount === 0 ? (
              <EmptyReason text={t("insights.resolve.empty")} />
            ) : (
              <>
                <Figure
                  label={t("insights.resolve.median")}
                  value={fmt(summary.timeToResolve.medianSeconds)}
                />
                <Figure
                  label={t("insights.resolve.p90")}
                  value={fmt(summary.timeToResolve.p90Seconds)}
                />
              </>
            )}
          </StatCard>

          <StatCard
            testId="insights-resolution-card"
            title={t("insights.resolution.title")}
          >
            {(["auto", "manual"] as const).map((kind) => {
              const entry = summary.byResolution[kind];
              return (
                <Figure
                  key={kind}
                  label={t(`insights.resolution.${kind}`)}
                  value={
                    entry.count === 0
                      ? "-"
                      : `${entry.count} · ${t("insights.resolution.median", {
                          duration: fmt(entry.medianSeconds),
                        })}`
                  }
                />
              );
            })}
            <Figure
              label={t("insights.resolution.other")}
              value={String(summary.byResolution.other.count)}
            />
          </StatCard>
        </div>
      </section>
    </>
  );
}

function EmptyReason({ text }: { text: string }) {
  return (
    <div>
      <div className="text-xl font-semibold">-</div>
      <p className="text-sm text-muted-foreground">{text}</p>
    </div>
  );
}
