import { useTranslation } from "react-i18next";
import {
  ResponsiveContainer,
  AreaChart,
  Area,
  XAxis,
  YAxis,
  Tooltip,
} from "recharts";
import type {
  AvailabilityStatus,
  AvailabilityThresholds,
  ResponseTimePoint,
  ResponseTimeSeries,
} from "@/api/hooks";
import {
  buildCombinedRows,
  expandTimeGaps,
  pointKey,
  statusFieldKey,
  type CombinedRow,
} from "@/lib/response-time-rollup";
import { computeTimeAxis, formatAxisTick } from "@/lib/chart-axis";
import {
  availabilityFill,
  classifyAvailabilityCounts,
  formatAvailabilityPct,
} from "@/lib/availability-status";

function formatDuration(ms: number) {
  if (ms >= 1000) {
    return `${(ms / 1000).toFixed(2)}s`;
  }
  return `${Math.round(ms)}ms`;
}

// Themed via the same `--status-*` CSS custom properties as statusStyle()
// (lib/status-style.ts) and badge.tsx, rather than hardcoded hex — those are
// the only two failure tones the rest of the app uses (error/destructive red,
// warning amber), so this collapses to the same two colors instead of
// inventing a third that has no dark-mode value and no badge equivalent.
function statusColor(status?: string) {
  switch (status) {
    case "down":
    case "error":
      return "var(--status-error)";
    case "timeout":
    case "warning":
    case "degraded":
      // "up, but something to report" / aggregated rollup — amber, neutral.
      return "var(--status-warning)";
    default:
      return "transparent";
  }
}

function CustomTooltip({
  active,
  payload,
}: {
  active?: boolean;
  payload?: Array<{ payload: ResponseTimePoint }>;
}) {
  const { t, i18n } = useTranslation();
  if (!active || !payload?.length) return null;
  const data = payload[0].payload;
  const date = new Date(data.time);
  const statusLabel =
    data.status && data.status !== "up"
      ? t(`status.${data.status}`, { defaultValue: data.status })
      : null;
  return (
    <div className="rounded-md border bg-background px-3 py-2 text-sm shadow-md">
      <p className="font-medium">
        {date.toLocaleDateString(i18n.language, {
          month: "short",
          day: "numeric",
        })}{" "}
        {date.toLocaleTimeString(i18n.language, {
          hour: "numeric",
          minute: "2-digit",
        })}
      </p>
      {data.durationP95 != null ? (
        <p className="text-xs text-muted-foreground">
          {formatDuration(data.durationP95)}
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">{t("noData")}</p>
      )}
      {statusLabel && (
        <p
          className="mt-1 text-xs font-medium"
          style={{ color: statusColor(data.status) }}
        >
          {statusLabel}
        </p>
      )}
    </div>
  );
}

// connectNulls={false} is deliberate — a gap in the data must read as a gap,
// not as a straight line across an outage. Its cost is that a sample whose
// neighbours are both null draws no line segment and so renders as nothing at
// all. Recharts has no "dot only where the line can't reach" mode, so decide
// per point: an isolated sample gets a dot, everything else stays clean.
interface DotRenderProps {
  cx?: number;
  cy?: number;
  index?: number;
  // recharts types this as React.Key | null; it is only forwarded, never read.
  key?: React.Key | null;
}

function isolatedDot(rows: readonly unknown[], key: string, color: string) {
  const hasValue = (index: number) =>
    (rows[index] as Record<string, unknown> | undefined)?.[key] != null;

  return function IsolatedDot(props: DotRenderProps) {
    const { cx, cy, index } = props;
    const i = index ?? -1;
    const isolated =
      cx != null && cy != null && hasValue(i) && !hasValue(i - 1) && !hasValue(i + 1);
    // An empty <g>, not null: recharts expects an element back from a dot
    // renderer.
    if (!isolated) return <g key={props.key} />;
    return (
      <circle key={props.key} cx={cx} cy={cy} r={2} fill={color} stroke="none" />
    );
  };
}

// Stable per-series chart color, cycling through the shared 5-color chart
// palette (same tokens dash0's response-time chart uses) in series order —
// the backend already returns series sorted by region, so this order is
// stable across renders/reloads.
function seriesColor(index: number): string {
  return `var(--chart-${(index % 5) + 1})`;
}

/** One cell of the availability strip drawn under the chart. */
interface AvailabilityCell {
  time: string;
  status: AvailabilityStatus;
  up: number;
  total: number;
  pct?: number;
}

/**
 * The colour-banded availability strip under the response-time chart.
 *
 * One cell per CHART POINT — which is why it is aligned to the chart's own time
 * slots with nothing left to reconcile: the point IS the slot (spec
 * 2026-08-26-10 phase 2). It replaces the old incident strip, which was drawn
 * from the probe's raw outcome and so answered "was it down" but never "how
 * much". The `ml-[50px] mr-[4px]` inset matches the chart's YAxis width and its
 * right margin, so a cell sits under the slice of the plot it describes.
 *
 * Each cell's flex weight is its slot's DURATION (spec 2026-09-21-03 B.4):
 * time to the next cell's start, and the last cell reuses the previous gap —
 * the chart is a real time axis now, so a uniform flex-1 would give six weeks
 * of dead region and sixteen minutes of live one the same width.
 *
 * Colours come from the shared availability vocabulary, so this strip and the
 * availability bar above it can never paint the same numbers differently.
 */
function AvailabilityStrip({
  cells,
  locale,
  noDataLabel,
  testId,
}: {
  cells: AvailabilityCell[];
  locale: string;
  noDataLabel: string;
  testId: string;
}) {
  if (cells.length === 0) return null;

  const starts = cells.map((cell) => Date.parse(cell.time));

  const weight = (index: number): number => {
    const next = starts[index + 1];
    if (Number.isFinite(next) && next > starts[index]) {
      return next - starts[index];
    }
    const previous = starts[index - 1];
    if (Number.isFinite(previous) && starts[index] > previous) {
      return starts[index] - previous;
    }
    return 1;
  };

  return (
    <div
      className="ml-[50px] mr-[4px] mt-1 flex h-1.5 w-auto overflow-hidden rounded-sm"
      data-testid={testId}
    >
      {cells.map((cell, index) => {
        const { fill, opacity } = availabilityFill(cell.status);
        const when = new Date(cell.time);
        const pctLabel = formatAvailabilityPct(cell.pct) ?? noDataLabel;
        const stamp = `${when.toLocaleDateString(locale, {
          month: "short",
          day: "numeric",
        })} ${when.toLocaleTimeString(locale, {
          hour: "numeric",
          minute: "2-digit",
        })}`;

        return (
          <div
            key={`${cell.time}-${index}`}
            style={{ backgroundColor: fill, opacity, flex: weight(index) }}
            data-status={cell.status}
            title={
              cell.total > 0
                ? `${stamp} — ${pctLabel} (${cell.up}/${cell.total})`
                : `${stamp} — ${noDataLabel}`
            }
          />
        );
      })}
    </div>
  );
}

interface ResponseTimeChartProps {
  series: ResponseTimeSeries[];
  /** The page's resolved availability thresholds. Only the multi-region path
   * needs them (it classifies a client-merged slot); the single-region path
   * uses the server's own per-point classification. */
  thresholds?: AvailabilityThresholds;
}

export function ResponseTimeChart({
  series,
  thresholds,
}: ResponseTimeChartProps) {
  const { t, i18n } = useTranslation();

  const isMultiSeries = series.length > 1;

  if (!isMultiSeries) {
    // Each row carries an epoch-ms `ts` so the axis is a real time axis (spec
    // 2026-09-21-03 B.1/B.2). Gap rows break the line wherever the series was
    // silent for several sampling intervals — connectNulls={false} only
    // breaks at null rows, and a silent stretch contributes none itself.
    const data = expandTimeGaps(
      (series[0]?.points ?? []).map((point) => ({
        ...point,
        ts: Date.parse(point.time),
      })),
      (time): ResponseTimePoint & { ts: number } => ({
        time,
        ts: Date.parse(time),
      }),
    );
    const hasData = data.some((d) => d.durationP95 != null);
    if (!hasData) return null;

    const axis = computeTimeAxis(data.map((d) => d.ts));
    const spanMs = axis.domain[1] - axis.domain[0];

    // The server already classified each point against the page's thresholds,
    // so the single-region strip renders exactly what it was told — no client
    // availability math at all.
    const singleSeriesCells: AvailabilityCell[] = data.map((point) => ({
      time: point.time,
      status: point.availabilityStatus ?? "noData",
      up: point.successfulChecks ?? 0,
      total: point.totalChecks ?? 0,
      pct: point.availabilityPct,
    }));

    return (
      <div className="mt-3">
        <p className="mb-1 text-xs text-muted-foreground">
          {t("responseTime")}
        </p>
        <ResponsiveContainer width="100%" height={100}>
          <AreaChart
            data={data}
            margin={{ top: 4, right: 4, bottom: 0, left: 4 }}
          >
            <defs>
              <linearGradient id="colorP95" x1="0" y1="0" x2="0" y2="1">
                <stop offset="5%" stopColor="var(--primary)" stopOpacity={0.3} />
                <stop
                  offset="95%"
                  stopColor="var(--primary)"
                  stopOpacity={0.05}
                />
              </linearGradient>
            </defs>
            <XAxis
              dataKey="ts"
              type="number"
              scale="time"
              domain={axis.domain}
              ticks={axis.ticks}
              tickFormatter={(v) =>
                formatAxisTick(Number(v), spanMs, i18n.language)
              }
              tick={{ fontSize: 10 }}
              tickLine={false}
              axisLine={false}
            />
            <YAxis
              tickFormatter={formatDuration}
              tick={{ fontSize: 10 }}
              tickLine={false}
              axisLine={false}
              width={50}
            />
            <Tooltip content={<CustomTooltip />} />
            <Area
              type="monotone"
              dataKey="durationP95"
              stroke="var(--primary)"
              strokeWidth={1.5}
              fill="url(#colorP95)"
              connectNulls={false}
              dot={isolatedDot(data, "durationP95", "var(--primary)")}
            />
          </AreaChart>
        </ResponsiveContainer>
        <AvailabilityStrip
          cells={singleSeriesCells}
          locale={i18n.language}
          noDataLabel={t("noData")}
          testId="response-time-chart-availability-strip"
        />
      </div>
    );
  }

  // Multi-series ("several regions") path below.
  const rows = buildCombinedRows(series);
  const hasData = rows.some((row) =>
    series.some((_, index) => row[pointKey(index)] != null),
  );
  if (!hasData) return null;

  // Each merged row carries an epoch-ms `ts` for the numeric axis (spec
  // 2026-09-21-03 B.1/B.2), and silent stretches become explicit null rows so
  // connectNulls={false} keeps rendering them as gaps.
  const chartRows = expandTimeGaps(
    rows.map((row) => ({ ...row, ts: Date.parse(row.time) })),
    (time): CombinedRow & { ts: number } => ({
      time,
      ts: Date.parse(time),
    }),
  );

  const axis = computeTimeAxis(
    chartRows.map((row) => row.ts).filter(Number.isFinite),
  );
  const spanMs = axis.domain[1] - axis.domain[0];

  // Several regions land in one slot, so the merged cell has to be classified
  // here — over the SUMMED up/total (buildCombinedRows), never over an average
  // of the regions' percentages, which is the same rule the server applies when
  // it buckets "all regions".
  const upThreshold = thresholds?.thresholdUp;
  const degradedThreshold = thresholds?.thresholdDegraded;
  const multiSeriesCells: AvailabilityCell[] = chartRows.map((row) => {
    const up = (row.availUp as number | undefined) ?? 0;
    const total = (row.availTotal as number | undefined) ?? 0;
    return {
      time: row.time,
      status: classifyAvailabilityCounts(
        up,
        total,
        upThreshold,
        degradedThreshold,
      ),
      up,
      total,
      pct: total > 0 ? (up / total) * 100 : undefined,
    };
  });

  const regionLabel = (region?: string) =>
    region || t("unknownRegion", { defaultValue: "Unknown region" });

  return (
    <div className="mt-3">
      <p className="mb-1 text-xs text-muted-foreground">{t("responseTime")}</p>
      <div
        className="mb-1.5 flex flex-wrap items-center gap-x-3 gap-y-1"
        data-testid="response-time-chart-legend"
      >
        {series.map((s, index) => (
          <span
            key={s.region ?? index}
            className="flex items-center gap-1 text-[11px] text-muted-foreground"
            data-testid="response-time-chart-legend-item"
          >
            <span
              className="inline-block h-2 w-2 shrink-0 rounded-sm"
              style={{ backgroundColor: seriesColor(index) }}
              aria-hidden="true"
            />
            <span translate="no">{regionLabel(s.region)}</span>
          </span>
        ))}
      </div>
      <ResponsiveContainer width="100%" height={100}>
        <AreaChart data={chartRows} margin={{ top: 4, right: 4, bottom: 0, left: 4 }}>
          <defs>
            {series.map((_, index) => (
              <linearGradient
                key={index}
                id={`colorRegion${index}`}
                x1="0"
                y1="0"
                x2="0"
                y2="1"
              >
                <stop
                  offset="5%"
                  stopColor={seriesColor(index)}
                  stopOpacity={0.3}
                />
                <stop
                  offset="95%"
                  stopColor={seriesColor(index)}
                  stopOpacity={0.05}
                />
              </linearGradient>
            ))}
          </defs>
          <XAxis
            dataKey="ts"
            type="number"
            scale="time"
            domain={axis.domain}
            ticks={axis.ticks}
            tickFormatter={(v) =>
              formatAxisTick(Number(v), spanMs, i18n.language)
            }
            tick={{ fontSize: 10 }}
            tickLine={false}
            axisLine={false}
          />
          <YAxis
            tickFormatter={formatDuration}
            tick={{ fontSize: 10 }}
            tickLine={false}
            axisLine={false}
            width={50}
          />
          <Tooltip
            content={({ active, payload }) => {
              if (!active || !payload?.length) return null;
              const row = payload[0]?.payload as CombinedRow | undefined;
              if (!row) return null;
              const date = new Date(row.time);
              return (
                <div className="rounded-md border bg-background px-3 py-2 text-sm shadow-md">
                  <p className="font-medium">
                    {date.toLocaleDateString(i18n.language, {
                      month: "short",
                      day: "numeric",
                    })}{" "}
                    {date.toLocaleTimeString(i18n.language, {
                      hour: "numeric",
                      minute: "2-digit",
                    })}
                  </p>
                  {series.map((s, index) => {
                    const value = row[pointKey(index)];
                    if (value == null) return null;
                    const status = row[statusFieldKey(index)] as
                      | string
                      | undefined;
                    return (
                      <div
                        key={index}
                        className="mt-1 flex items-center gap-1.5 text-xs"
                      >
                        <span
                          className="inline-block h-2 w-2 shrink-0 rounded-sm"
                          style={{ backgroundColor: seriesColor(index) }}
                          aria-hidden="true"
                        />
                        <span className="text-muted-foreground" translate="no">
                          {regionLabel(s.region)}
                        </span>
                        <span>{formatDuration(value as number)}</span>
                        {status && status !== "up" && (
                          <span
                            className="font-medium"
                            style={{ color: statusColor(status) }}
                          >
                            {t(`status.${status}`, { defaultValue: status })}
                          </span>
                        )}
                      </div>
                    );
                  })}
                </div>
              );
            }}
          />
          {series.map((_, index) => (
            <Area
              key={index}
              type="monotone"
              dataKey={pointKey(index)}
              stroke={seriesColor(index)}
              strokeWidth={1.5}
              fill={`url(#colorRegion${index})`}
              connectNulls={false}
              dot={isolatedDot(chartRows, pointKey(index), seriesColor(index))}
            />
          ))}
        </AreaChart>
      </ResponsiveContainer>
      <AvailabilityStrip
        cells={multiSeriesCells}
        locale={i18n.language}
        noDataLabel={t("noData")}
        testId="response-time-chart-availability-strip"
      />
    </div>
  );
}
