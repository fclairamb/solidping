import { useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { ZoomOut } from "lucide-react";
import {
  Area,
  AreaChart,
  CartesianGrid,
  Legend,
  ReferenceArea,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
  type MouseHandlerDataParam,
} from "recharts";
import { format } from "date-fns";

import { useChartWindowResults } from "@/api/hooks";
import type { TimeRange, ZoomWindow } from "@/lib/chart-window";
import { HTTP_TIMING_PHASES } from "@/lib/http-timing";
import { buildTimingSeries } from "@/lib/http-timing-series";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ChartRangeControl } from "@/components/checks/chart-range-control";
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

// Legend and tooltip follow the request order, not Recharts' alphabetical sort.
const PHASE_ORDER: string[] = HTTP_TIMING_PHASES.map(({ id }) => id);

interface TimingChartProps {
  org: string;
  checkUid: string;
  periodMs?: number;
  timeRange: TimeRange;
  // Zoom window shared with the response time chart (graphFrom/graphTo).
  zoomFrom?: number;
  zoomTo?: number;
  // Called on drag-release with the selected window, or with no window to reset.
  onZoomChange?: (from?: number, to?: number) => void;
}

/** A zoom is active only for a well-formed forward window. */
function zoomWindow(from?: number, to?: number): ZoomWindow | undefined {
  return from != null && to != null && to > from ? { from, to } : undefined;
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
  zoomFrom,
  zoomTo,
  onZoomChange,
}: TimingChartProps) {
  const { t } = useTranslation("checks");
  const zoom = zoomWindow(zoomFrom, zoomTo);
  const { data, isLoading, isEmptyPending } = useChartWindowResults(
    org,
    checkUid,
    { timeRange, periodMs, zoom },
    { withMetrics: true },
  );
  // Drag-to-select zoom, same gesture as the response time chart. The refs
  // hold the bounds for the release handler, the state only draws the band.
  const dragStartRef = useRef<number | null>(null);
  const dragEndRef = useRef<number | null>(null);
  const [dragLeft, setDragLeft] = useState<number | null>(null);
  const [dragRight, setDragRight] = useState<number | null>(null);

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

  const activeTs = (state: MouseHandlerDataParam): number | null => {
    const ts = Number(state?.activeLabel);
    return Number.isFinite(ts) ? ts : null;
  };
  const handleSelectStart = (state: MouseHandlerDataParam) => {
    const ts = activeTs(state);
    if (ts == null) return;
    dragStartRef.current = ts;
    dragEndRef.current = null;
    setDragLeft(ts);
    setDragRight(null);
  };
  const handleSelectMove = (state: MouseHandlerDataParam) => {
    if (dragStartRef.current == null) return;
    const ts = activeTs(state);
    if (ts == null) return;
    dragEndRef.current = ts;
    setDragRight(ts);
  };
  const handleSelectEnd = () => {
    const start = dragStartRef.current;
    const end = dragEndRef.current;
    dragStartRef.current = null;
    dragEndRef.current = null;
    setDragLeft(null);
    setDragRight(null);
    if (start == null || end == null) return;
    const from = Math.min(start, end);
    const to = Math.max(start, end);
    // A near-zero drag is a click: require 1% of the visible span (floor 1s).
    if (to - from < Math.max(spanMs * 0.01, 1000)) return;
    onZoomChange?.(from, to);
  };

  return (
    <div
      className="h-64 w-full select-none"
      data-testid="timing-chart"
      // Double-click anywhere on the chart resets an active zoom.
      onDoubleClick={() => {
        if (zoom) onZoomChange?.(undefined, undefined);
      }}
    >
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart
          data={series}
          onMouseDown={handleSelectStart}
          onMouseMove={handleSelectMove}
          onMouseUp={handleSelectEnd}
          onMouseLeave={handleSelectEnd}
          onTouchStart={handleSelectStart}
          onTouchMove={handleSelectMove}
          onTouchEnd={handleSelectEnd}
        >
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
          {dragLeft != null && dragRight != null && (
            <ReferenceArea
              x1={Math.min(dragLeft, dragRight)}
              x2={Math.max(dragLeft, dragRight)}
              fill="var(--primary)"
              fillOpacity={0.12}
              stroke="var(--primary)"
              strokeOpacity={0.4}
            />
          )}
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
        <div className="flex flex-wrap items-center gap-2 sm:gap-3">
          {zoomWindow(chart.zoomFrom, chart.zoomTo) && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => chart.onZoomChange?.(undefined, undefined)}
              className="gap-1 px-2 text-xs"
              data-testid="timing-chart-reset-zoom"
            >
              <ZoomOut className="h-3.5 w-3.5" />
              <span className="hidden sm:inline">
                {t("detail.chart.resetZoom")}
              </span>
            </Button>
          )}
          <ChartRangeControl
            value={chart.timeRange}
            onChange={onTimeRangeChange}
            testIdPrefix="timing-range"
          />
        </div>
      </CardHeader>
      <CardContent>
        <TimingChart {...chart} />
      </CardContent>
    </Card>
  );
}
