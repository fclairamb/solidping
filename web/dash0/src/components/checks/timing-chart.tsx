import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import {
  Area,
  AreaChart,
  CartesianGrid,
  Legend,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { format } from "date-fns";

import { useChartWindowResults } from "@/api/hooks";
import type { TimeRange } from "@/lib/chart-window";
import { HTTP_TIMING_PHASES } from "@/lib/http-timing";
import { buildTimingSeries } from "@/lib/http-timing-series";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";

const PHASE_COLORS: Record<string, string> = {
  redirect: "var(--chart-5, #a855f7)",
  dns: "var(--chart-1, #3b82f6)",
  connect: "var(--chart-2, #10b981)",
  tls: "var(--chart-3, #f59e0b)",
  ttfb: "var(--chart-4, #ef4444)",
  download: "var(--chart-6, #64748b)",
};

// Legend and tooltip follow the request order, not Recharts' alphabetical sort.
const PHASE_ORDER: string[] = HTTP_TIMING_PHASES.map(({ id }) => id);

interface TimingChartProps {
  org: string;
  checkUid: string;
  periodMs?: number;
  timeRange: TimeRange;
}

/**
 * Stacked areas of the per-bucket HTTP phase averages (spec 2026-10-12-08).
 * Mounted only while the Timing view is selected: it is the one chart that
 * asks for `with=metrics`, so the default response-time plan stays blob-free.
 */
export function TimingChart({
  org,
  checkUid,
  periodMs,
  timeRange,
}: TimingChartProps) {
  const { t } = useTranslation("checks");
  const { data, isLoading, isEmptyPending } = useChartWindowResults(
    org,
    checkUid,
    { timeRange, periodMs },
    { withMetrics: true },
  );

  const series = useMemo(() => buildTimingSeries(data.data), [data.data]);

  if (isLoading || isEmptyPending) {
    return (
      <Skeleton className="h-64 w-full" data-testid="timing-chart-loading" />
    );
  }

  if (series.length === 0) {
    return (
      <div
        className="flex h-64 items-center justify-center text-sm text-muted-foreground"
        data-testid="timing-chart-empty"
      >
        {t("timing.noData")}
      </div>
    );
  }

  const spanMs = series[series.length - 1].ts - series[0].ts;
  const fmt = spanMs > 36 * 3600_000 ? "MMM d" : "HH:mm";

  return (
    <div className="h-64 w-full" data-testid="timing-chart">
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={series}>
          <CartesianGrid strokeDasharray="3 3" className="stroke-muted" />
          <XAxis
            dataKey="ts"
            type="number"
            domain={["dataMin", "dataMax"]}
            tickFormatter={(v: number) => format(new Date(v), fmt)}
            className="text-xs"
            tick={{ fill: "var(--muted-foreground)" }}
          />
          <YAxis
            unit=" ms"
            width={64}
            className="text-xs"
            tick={{ fill: "var(--muted-foreground)" }}
          />
          <Tooltip
            contentStyle={{
              background: "var(--popover)",
              color: "var(--popover-foreground)",
              border: "1px solid var(--border)",
              borderRadius: "var(--radius-md)",
              fontSize: "0.875rem",
            }}
            labelStyle={{ color: "var(--muted-foreground)" }}
            itemStyle={{ color: "var(--popover-foreground)" }}
            itemSorter={(item) => PHASE_ORDER.indexOf(String(item.dataKey))}
            labelFormatter={(v) => format(new Date(Number(v)), "PPpp")}
            formatter={(value, name) => [
              `${Math.round(Number(value) * 100) / 100} ms`,
              t(`timing.phase.${String(name)}`),
            ]}
          />
          <Legend
            iconType="square"
            itemSorter={null}
            formatter={(value) => (
              <span className="text-xs text-muted-foreground">
                {t(`timing.phase.${String(value)}`)}
              </span>
            )}
          />
          {HTTP_TIMING_PHASES.map(({ id }) => (
            <Area
              key={id}
              type="monotone"
              dataKey={id}
              name={id}
              stackId="timing"
              stroke={PHASE_COLORS[id]}
              fill={PHASE_COLORS[id]}
              fillOpacity={0.5}
              isAnimationActive={false}
              connectNulls
            />
          ))}
        </AreaChart>
      </ResponsiveContainer>
    </div>
  );
}

export type ChartView = "response" | "timing";

interface ChartViewSwitchProps {
  value: ChartView;
  onChange: (view: ChartView) => void;
}

/**
 * The "Response time | Timing" switch shown above the chart of an HTTP check.
 * Same default/outline buttons as the charts' Hour / Day / Week / Month range.
 */
export function ChartViewSwitch({ value, onChange }: ChartViewSwitchProps) {
  const { t } = useTranslation("checks");
  const views: { view: ChartView; label: string }[] = [
    { view: "response", label: t("timing.viewResponse") },
    { view: "timing", label: t("timing.viewTiming") },
  ];
  return (
    <div
      role="group"
      aria-label={t("timing.viewLabel")}
      className="flex items-center gap-1"
    >
      {views.map(({ view, label }) => (
        <Button
          key={view}
          variant={value === view ? "default" : "outline"}
          size="sm"
          aria-pressed={value === view}
          onClick={() => onChange(view)}
          className="px-2 text-xs sm:px-3 sm:text-sm"
          data-testid={`chart-view-${view}`}
        >
          {label}
        </Button>
      ))}
    </div>
  );
}

const RANGES: TimeRange[] = ["hour", "day", "week", "month"];

function rangeKey(range: TimeRange): string {
  return `detail.chart.range${range[0].toUpperCase()}${range.slice(1)}`;
}

interface TimingChartCardProps extends TimingChartProps {
  onTimeRangeChange: (range: TimeRange) => void;
}

/**
 * The Timing view: replaces the response time chart while selected. The
 * "Response time | Timing" switch stays the page-level one above the card.
 */
export function TimingChartCard({
  onTimeRangeChange,
  ...chart
}: TimingChartCardProps) {
  const { t } = useTranslation("checks");
  return (
    <Card data-testid="timing-chart-card">
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle>{t("timing.chartTitle")}</CardTitle>
        <div className="flex items-center gap-1">
          {RANGES.map((range) => (
            <Button
              key={range}
              variant={chart.timeRange === range ? "default" : "outline"}
              size="sm"
              aria-pressed={chart.timeRange === range}
              onClick={() => onTimeRangeChange(range)}
              className="px-2 text-xs sm:px-3 sm:text-sm"
              data-testid={`timing-range-${range}`}
            >
              <span className="sm:hidden">{t(`${rangeKey(range)}Short`)}</span>
              <span className="hidden sm:inline">{t(rangeKey(range))}</span>
            </Button>
          ))}
        </div>
      </CardHeader>
      <CardContent>
        <TimingChart {...chart} />
      </CardContent>
    </Card>
  );
}
