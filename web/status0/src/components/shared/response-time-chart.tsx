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
import {
  buildCombinedRows,
  pointKey,
  statusFieldKey,
  type CombinedRow,
} from "@/lib/response-time-rollup";

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

// Recharts puts one tick per category by default, so a multi-day window
// sampled every few hours printed the *same* day-only label over and over
// ("Aug 9  Aug 9  Aug 9  Aug 10 …"). Pick a small, evenly spaced tick set
// ourselves instead of letting the tick count follow the sample count.
const MAX_TICKS = 5;

function pickTicks(times: string[]): string[] {
  if (times.length <= MAX_TICKS) return times;
  const step = (times.length - 1) / (MAX_TICKS - 1);
  const picked = Array.from(
    { length: MAX_TICKS },
    (_, i) => times[Math.round(i * step)],
  );
  return [...new Set(picked)];
}

// Label the picked ticks, falling back to a time-of-day label whenever a tick
// would repeat the day printed by the tick before it — so the axis reads
// "Jul 28 · Aug 3 · Aug 9 · Aug 11 · 18:00" rather than the same date twice.
function buildTickLabels(
  ticks: string[],
  spansDays: boolean,
  locale: string,
): Record<string, string> {
  const labels: Record<string, string> = {};
  let previousDay = "";
  for (const tick of ticks) {
    const date = new Date(tick);
    if (!spansDays) {
      labels[tick] = formatTick(tick, false, locale);
      continue;
    }
    const day = date.toLocaleDateString(locale, {
      month: "short",
      day: "numeric",
    });
    labels[tick] =
      day === previousDay
        ? date.toLocaleTimeString(locale, { hour: "numeric", minute: "2-digit" })
        : day;
    previousDay = day;
  }
  return labels;
}

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

    const ticks = pickTicks(data.map((d) => d.time));
    const tickLabels = buildTickLabels(ticks, spansDays, i18n.language);

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
              ticks={ticks}
              tickFormatter={(v) =>
                tickLabels[v] ?? formatTick(v, spansDays, i18n.language)
              }
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
              dot={isolatedDot(data, "durationP95", "var(--primary)")}
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

  const ticks = pickTicks(rows.map((row) => row.time));
  const tickLabels = buildTickLabels(ticks, spansDays, i18n.language);

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
            ticks={ticks}
            tickFormatter={(v) =>
              tickLabels[v] ?? formatTick(v, spansDays, i18n.language)
            }
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
              dot={isolatedDot(rows, pointKey(index), seriesColor(index))}
            />
          ))}
        </AreaChart>
      </ResponsiveContainer>
      {hasIncidents && (
        <div
          className="ml-[50px] mr-[4px] mt-1 flex h-1.5 w-auto overflow-hidden rounded-sm"
          aria-hidden="true"
          data-testid="response-time-chart-incident-strip"
        >
          {rows.map((row, idx) => {
            const color = statusColor(row.status);
            return (
              <div
                key={`${row.time}-${idx}`}
                className="flex-1"
                style={{ backgroundColor: color }}
                data-status={row.status ?? ""}
              />
            );
          })}
        </div>
      )}
    </div>
  );
}
