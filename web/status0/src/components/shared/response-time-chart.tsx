import { useTranslation } from "react-i18next";
import {
  ResponsiveContainer,
  AreaChart,
  Area,
  XAxis,
  YAxis,
  Tooltip,
} from "recharts";
import type { ResponseTimePoint } from "@/api/hooks";

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

interface ResponseTimeChartProps {
  data: ResponseTimePoint[];
}

export function ResponseTimeChart({ data }: ResponseTimeChartProps) {
  const { t, i18n } = useTranslation();
  const hasData = data.some((d) => d.durationP95 != null);
  if (!hasData) return null;

  // Determine if data spans multiple days to adapt x-axis formatting
  const first = data[0]?.time;
  const last = data[data.length - 1]?.time;
  const spansDays =
    first && last
      ? new Date(last).getTime() - new Date(first).getTime() >
        24 * 60 * 60 * 1000
      : false;

  const hasIncidents = data.some(
    (d) => d.status === "down" || d.status === "timeout" || d.status === "error",
  );

  return (
    <div className="mt-3">
      <p className="mb-1 text-xs text-muted-foreground">{t("responseTime")}</p>
      <ResponsiveContainer width="100%" height={100}>
        <AreaChart data={data} margin={{ top: 4, right: 4, bottom: 0, left: 4 }}>
          <defs>
            <linearGradient id="colorP95" x1="0" y1="0" x2="0" y2="1">
              <stop offset="5%" stopColor="var(--primary)" stopOpacity={0.3} />
              <stop offset="95%" stopColor="var(--primary)" stopOpacity={0.05} />
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
