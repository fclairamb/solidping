import { useTranslation } from "react-i18next";
import {
  ResponsiveContainer,
  AreaChart,
  Area,
  XAxis,
  YAxis,
  Tooltip,
} from "recharts";
import type { ResponseTimePoint, ResponseTimeSeries } from "@/api/hooks";

function formatTick(isoStr: string, spansDays: boolean, locale: string) {
  const date = new Date(isoStr);
  if (spansDays) {
    return date.toLocaleDateString(locale, {
      month: "short",
      day: "numeric",
    });
  }
  return date.toLocaleTimeString(locale, {
    hour: "numeric",
    minute: "2-digit",
  });
}

function formatDuration(ms: number) {
  if (ms >= 1000) {
    return `${(ms / 1000).toFixed(2)}s`;
  }
  return `${Math.round(ms)}ms`;
}

function statusColor(status?: string) {
  switch (status) {
    case "down":
      return "#ef4444";
    case "timeout":
      return "#facc15";
    case "error":
      return "#f97316";
    case "warning":
    case "degraded":
      // "up, but something to report" / aggregated rollup — amber, neutral.
      return "#facc15";
    default:
      return "transparent";
  }
}

// Severity rank used to roll several regions' statuses up into ONE incident
// strip color for a shared timestamp — "worst status wins" (spec
// 2026-08-14-04). Higher wins; anything not listed (up, created, running,
// undefined) ranks 0, i.e. never overrides a real incident.
const STATUS_SEVERITY: Record<string, number> = {
  down: 4,
  error: 3,
  timeout: 2,
  degraded: 1,
  warning: 1,
};

function severityRank(status?: string): number {
  if (!status) return 0;
  return STATUS_SEVERITY[status] ?? 0;
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

// Stable per-series chart color, cycling through the shared 5-color chart
// palette (same tokens dash0's response-time chart uses) in series order —
// the backend already returns series sorted by region, so this order is
// stable across renders/reloads.
function seriesColor(index: number): string {
  return `var(--chart-${(index % 5) + 1})`;
}

function pointKey(index: number): string {
  return `p${index}`;
}

function statusFieldKey(index: number): string {
  return `st${index}`;
}

interface CombinedRow {
  time: string;
  status?: string;
  [field: string]: string | number | null | undefined;
}

// Pivots per-region series into ONE array of rows keyed by the sorted union
// of timestamps across every series (recharts needs one shared data array to
// render several Areas against the same x-axis). Each row also carries a
// rolled-up `status` — the worst status among the regions with a point at
// that exact timestamp — which drives the single incident strip.
function buildCombinedRows(series: ResponseTimeSeries[]): CombinedRow[] {
  const byTime = new Map<string, CombinedRow>();

  series.forEach((s, index) => {
    for (const point of s.points) {
      let row = byTime.get(point.time);
      if (!row) {
        row = { time: point.time };
        byTime.set(point.time, row);
      }
      row[pointKey(index)] = point.durationP95 ?? null;
      row[statusFieldKey(index)] = point.status;
    }
  });

  const rows = Array.from(byTime.values()).sort((a, b) =>
    a.time.localeCompare(b.time),
  );

  for (const row of rows) {
    let worstStatus: string | undefined;
    let worstRank = -1;
    series.forEach((_, index) => {
      const status = row[statusFieldKey(index)] as string | undefined;
      const rank = severityRank(status);
      if (rank > worstRank) {
        worstRank = rank;
        worstStatus = status;
      }
    });
    row.status = worstStatus;
  }

  return rows;
}

interface ResponseTimeChartProps {
  series: ResponseTimeSeries[];
}

export function ResponseTimeChart({ series }: ResponseTimeChartProps) {
  const { t, i18n } = useTranslation();

  const isMultiSeries = series.length > 1;

  if (!isMultiSeries) {
    const data = series[0]?.points ?? [];
    const hasData = data.some((d) => d.durationP95 != null);
    if (!hasData) return null;

    const first = data[0]?.time;
    const last = data[data.length - 1]?.time;
    const spansDays =
      first && last
        ? new Date(last).getTime() - new Date(first).getTime() >
          24 * 60 * 60 * 1000
        : false;

    const hasIncidents = data.some(
      (d) =>
        d.status === "down" || d.status === "timeout" || d.status === "error",
    );

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
              dataKey="time"
              tickFormatter={(v) => formatTick(v, spansDays, i18n.language)}
              tick={{ fontSize: 10 }}
              tickLine={false}
              axisLine={false}
              interval="preserveStartEnd"
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
            />
          </AreaChart>
        </ResponsiveContainer>
        {hasIncidents && (
          <div
            className="ml-[50px] mr-[4px] mt-1 flex h-1.5 w-auto overflow-hidden rounded-sm"
            aria-hidden="true"
          >
            {data.map((point, idx) => {
              const color = statusColor(point.status);
              return (
                <div
                  key={`${point.time}-${idx}`}
                  className="flex-1"
                  style={{ backgroundColor: color }}
                />
              );
            })}
          </div>
        )}
      </div>
    );
  }

  // Multi-series ("several regions") path below.
  const rows = buildCombinedRows(series);
  const hasData = rows.some((row) =>
    series.some((_, index) => row[pointKey(index)] != null),
  );
  if (!hasData) return null;

  const first = rows[0]?.time;
  const last = rows[rows.length - 1]?.time;
  const spansDays =
    first && last
      ? new Date(last).getTime() - new Date(first).getTime() >
        24 * 60 * 60 * 1000
      : false;

  const hasIncidents = rows.some(
    (row) =>
      row.status === "down" ||
      row.status === "timeout" ||
      row.status === "error",
  );

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
        <AreaChart data={rows} margin={{ top: 4, right: 4, bottom: 0, left: 4 }}>
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
            dataKey="time"
            tickFormatter={(v) => formatTick(v, spansDays, i18n.language)}
            tick={{ fontSize: 10 }}
            tickLine={false}
            axisLine={false}
            interval="preserveStartEnd"
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
            />
          ))}
        </AreaChart>
      </ResponsiveContainer>
      {hasIncidents && (
        <div
          className="ml-[50px] mr-[4px] mt-1 flex h-1.5 w-auto overflow-hidden rounded-sm"
          aria-hidden="true"
        >
          {rows.map((row, idx) => {
            const color = statusColor(row.status);
            return (
              <div
                key={`${row.time}-${idx}`}
                className="flex-1"
                style={{ backgroundColor: color }}
              />
            );
          })}
        </div>
      )}
    </div>
  );
}
