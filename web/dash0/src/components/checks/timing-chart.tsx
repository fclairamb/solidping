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
import { SegmentedControl } from "@/components/ui/segmented-control";
import { Skeleton } from "@/components/ui/skeleton";

const PHASE_COLORS: Record<string, string> = {
  redirect: "var(--chart-5, #a855f7)",
  dns: "var(--chart-1, #3b82f6)",
  connect: "var(--chart-2, #10b981)",
  tls: "var(--chart-3, #f59e0b)",
  ttfb: "var(--chart-4, #ef4444)",
  download: "var(--chart-6, #64748b)",
};

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
            labelFormatter={(v) => format(new Date(Number(v)), "PPpp")}
            formatter={(value, name) => [
              `${Math.round(Number(value) * 100) / 100} ms`,
              t(`timing.phase.${String(name)}`),
            ]}
          />
          <Legend
            iconType="square"
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

/** The "Response time | Timing" switch shown above the chart of an HTTP check. */
export function ChartViewSwitch({ value, onChange }: ChartViewSwitchProps) {
  const { t } = useTranslation("checks");
  return (
    <SegmentedControl<ChartView>
      value={value}
      onValueChange={onChange}
      aria-label={t("timing.viewLabel")}
      options={[
        {
          value: "response",
          label: t("timing.viewResponse"),
          testId: "chart-view-response",
        },
        {
          value: "timing",
          label: t("timing.viewTiming"),
          testId: "chart-view-timing",
        },
      ]}
    />
  );
}

const RANGES: TimeRange[] = ["hour", "day", "week", "month"];

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
        <SegmentedControl<TimeRange>
          value={chart.timeRange}
          onValueChange={onTimeRangeChange}
          aria-label={t("timing.chartTitle")}
          options={RANGES.map((r) => ({
            value: r,
            label: t(`detail.chart.range${r[0].toUpperCase()}${r.slice(1)}`),
            testId: `timing-range-${r}`,
          }))}
        />
      </CardHeader>
      <CardContent>
        <TimingChart {...chart} />
      </CardContent>
    </Card>
  );
}
