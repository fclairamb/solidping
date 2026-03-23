import { useState, useMemo } from "react";
import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  ReferenceArea,
} from "recharts";
import { format, subDays, subHours, startOfMinute } from "date-fns";
import { useAllResults } from "@/api/hooks";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";

type TimeRange = "hour" | "day" | "week" | "month";

interface ResponseTimeChartProps {
  org: string;
  checkUid: string;
  refetchInterval?: number;
  initialPeriod?: TimeRange;
  initialFullRange?: boolean;
  onSettingsChange?: (period: TimeRange, fullRange: boolean) => void;
}

interface ChartPoint {
  ts: number;
  durationMs: number | null;
  status: string;
}

interface GapRegion {
  x1: number;
  x2: number;
  showLabel: boolean;
}

function getStartFor(range: TimeRange): string {
  const now = startOfMinute(new Date());
  switch (range) {
    case "hour":
      return subHours(now, 1).toISOString();
    case "day":
      return subHours(now, 24).toISOString();
    case "week":
      return subDays(now, 7).toISOString();
    case "month":
      return subDays(now, 30).toISOString();
  }
}

/** Choose x-axis format based on the actual time span */
function adaptiveFormat(tsMs: number, spanMs: number): string {
  const date = new Date(tsMs);
  const ONE_HOUR = 3_600_000;
  const ONE_DAY = 24 * ONE_HOUR;

  if (spanMs < ONE_HOUR) return format(date, "HH:mm:ss");
  if (spanMs < ONE_DAY) return format(date, "HH:mm");
  if (spanMs < 7 * ONE_DAY) return format(date, "EEE HH:mm");
  return format(date, "MMM d");
}

function formatMs(value: number): string {
  if (value >= 1000) return `${(value / 1000).toFixed(1)}s`;
  return `${Math.round(value)}ms`;
}

/** Compute median of a sorted array of numbers */
function median(sorted: number[]): number {
  if (sorted.length === 0) return 0;
  const mid = Math.floor(sorted.length / 2);
  return sorted.length % 2 === 0
    ? (sorted[mid - 1] + sorted[mid]) / 2
    : sorted[mid];
}

/** Detect gaps in data where interval exceeds 5x the median check interval */
function detectGaps(
  data: ChartPoint[],
  domainMin: number,
  domainMax: number,
): GapRegion[] {
  if (data.length < 2) return [];

  // Calculate intervals between consecutive points
  const intervals: number[] = [];
  for (let i = 1; i < data.length; i++) {
    intervals.push(data[i].ts - data[i - 1].ts);
  }

  // Sort for median
  const sortedIntervals = [...intervals].sort((a, b) => a - b);
  const medianInterval = median(sortedIntervals);
  if (medianInterval === 0) return [];

  const gapThreshold = medianInterval * 5;
  const domainSpan = domainMax - domainMin;
  const gaps: GapRegion[] = [];

  for (let i = 1; i < data.length; i++) {
    const interval = data[i].ts - data[i - 1].ts;
    if (interval > gapThreshold) {
      const gapWidth = data[i].ts - data[i - 1].ts;
      gaps.push({
        x1: data[i - 1].ts,
        x2: data[i].ts,
        showLabel: domainSpan > 0 && gapWidth / domainSpan > 0.1,
      });
    }
  }

  return gaps;
}

/** Insert null markers at gap boundaries to break the line */
function insertGapMarkers(
  data: ChartPoint[],
  gaps: GapRegion[],
): ChartPoint[] {
  if (gaps.length === 0) return data;

  const gapStarts = new Set(gaps.map((g) => g.x1));
  const gapEnds = new Set(gaps.map((g) => g.x2));
  const result: ChartPoint[] = [];

  for (const point of data) {
    // Insert null after a gap-start point
    if (gapStarts.has(point.ts)) {
      result.push(point);
      result.push({ ts: point.ts + 1, durationMs: null, status: "up" });
    }
    // Insert null before a gap-end point
    else if (gapEnds.has(point.ts)) {
      result.push({ ts: point.ts - 1, durationMs: null, status: "up" });
      result.push(point);
    } else {
      result.push(point);
    }
  }

  return result;
}

/** Compute smart x-axis ticks based on data density */
function computeTicks(
  domainMin: number,
  domainMax: number,
  data: ChartPoint[],
): number[] {
  const domainSpan = domainMax - domainMin;
  if (domainSpan <= 0) return [];

  const realPoints = data.filter((p) => p.durationMs != null);
  if (realPoints.length === 0) return [domainMin, domainMax];

  const dataMin = realPoints[0].ts;
  const dataMax = realPoints[realPoints.length - 1].ts;
  const dataSpan = dataMax - dataMin;

  // Sparse data: less than 30% of the domain has data
  if (dataSpan / domainSpan < 0.3) {
    const ticks = [domainMin];
    if (dataMin > domainMin) ticks.push(dataMin);
    // Add a few ticks within the data cluster
    const clusterTicks = 3;
    for (let i = 1; i < clusterTicks; i++) {
      ticks.push(dataMin + (dataSpan * i) / clusterTicks);
    }
    ticks.push(dataMax);
    if (dataMax < domainMax) ticks.push(domainMax);
    return ticks;
  }

  // Normal data: evenly-spaced ticks
  const tickCount = 6;
  const ticks: number[] = [];
  for (let i = 0; i <= tickCount; i++) {
    ticks.push(domainMin + (domainSpan * i) / tickCount);
  }
  return ticks;
}

export function ResponseTimeChart({
  org,
  checkUid,
  refetchInterval,
  initialPeriod,
  initialFullRange,
  onSettingsChange,
}: ResponseTimeChartProps) {
  const [timeRange, setTimeRange] = useState<TimeRange>(initialPeriod ?? "day");
  const [fullRange, setFullRange] = useState(initialFullRange ?? false);

  const updateTimeRange = (range: TimeRange) => {
    setTimeRange(range);
    onSettingsChange?.(range, fullRange);
  };

  const updateFullRange = (full: boolean) => {
    setFullRange(full);
    onSettingsChange?.(timeRange, full);
  };

  const periodStartAfter = useMemo(() => getStartFor(timeRange), [timeRange]);

  // Use hourly aggregations for longer ranges to avoid fetching thousands of raw results
  const periodType = timeRange === "week" || timeRange === "month" ? "hour" : "raw";

  const { data: results, isLoading } = useAllResults(org, {
    checkUid,
    periodStartAfter,
    periodType,
    with: "durationMs,region",
    size: 1000,
    refetchInterval,
  });

  const { chartData, regions, formatSpanMs, domainMin, domainMax, gaps } =
    useMemo(() => {
      const hasData = !!results?.data?.length;

      if (!hasData && !fullRange)
        return {
          chartData: [] as ChartPoint[],
          regions: [] as string[],
          formatSpanMs: 0,
          domainMin: 0,
          domainMax: 0,
          gaps: [] as GapRegion[],
        };

      const regionSet = new Set<string>();
      const sorted = [...(results?.data ?? [])].reverse();

      const data: ChartPoint[] = sorted
        .filter((r) => r.periodStart)
        .map((r) => {
          if (r.region) regionSet.add(r.region);
          return {
            ts: new Date(r.periodStart!).getTime(),
            durationMs: r.durationMs ?? 0,
            status: r.status ?? "up",
          };
        });

      if (fullRange) {
        const rangeStartMs = new Date(periodStartAfter).getTime();
        const rangeEndMs = startOfMinute(new Date()).getTime();
        const fullSpan = rangeEndMs - rangeStartMs;

        // Detect gaps in the real data
        const detectedGaps = detectGaps(data, rangeStartMs, rangeEndMs);

        // Insert gap markers at detected boundaries
        const dataWithGapMarkers = insertGapMarkers(data, detectedGaps);

        // Build full-range points with boundary markers
        const points: ChartPoint[] = [];

        // Start boundary
        points.push({ ts: rangeStartMs, durationMs: null, status: "up" });

        if (dataWithGapMarkers.length > 0) {
          // Gap marker just before first real point
          const firstReal = dataWithGapMarkers.find(
            (p) => p.durationMs != null,
          );
          if (firstReal && firstReal.ts - rangeStartMs > 1) {
            points.push({
              ts: firstReal.ts - 1,
              durationMs: null,
              status: "up",
            });
          }
          // Real data (with gap markers already inserted)
          points.push(...dataWithGapMarkers);
          // Gap marker just after last real point
          const lastReal = [...dataWithGapMarkers]
            .reverse()
            .find((p) => p.durationMs != null);
          if (lastReal && rangeEndMs - lastReal.ts > 1) {
            points.push({
              ts: lastReal.ts + 1,
              durationMs: null,
              status: "up",
            });
          }
        }

        // End boundary
        points.push({ ts: rangeEndMs, durationMs: null, status: "up" });

        // Use actual data span for tick formatting (cluster-aware)
        const dataTs = data.map((d) => d.ts);
        const actualDataSpan =
          dataTs.length > 1 ? Math.max(...dataTs) - Math.min(...dataTs) : 0;

        return {
          chartData: points,
          regions: Array.from(regionSet),
          formatSpanMs: actualDataSpan || fullSpan,
          domainMin: rangeStartMs,
          domainMax: rangeEndMs,
          gaps: detectedGaps,
        };
      }

      // Non-full-range: compute span from data + detect gaps
      const tsList = data.map((d) => d.ts);
      const min = tsList.length ? Math.min(...tsList) : 0;
      const max = tsList.length ? Math.max(...tsList) : 0;
      const span = max - min;

      const detectedGaps = detectGaps(data, min, max);
      const dataWithGapMarkers = insertGapMarkers(data, detectedGaps);

      return {
        chartData: dataWithGapMarkers,
        regions: Array.from(regionSet),
        formatSpanMs: span,
        domainMin: min,
        domainMax: max,
        gaps: detectedGaps,
      };
    }, [results, fullRange, periodStartAfter]);

  const ticks = useMemo(
    () => computeTicks(domainMin, domainMax, chartData),
    [domainMin, domainMax, chartData],
  );

  const COLOR_UP = "hsl(142, 76%, 36%)";
  const COLOR_DOWN = "hsl(0, 72%, 51%)";

  const gradientStops = useMemo(() => {
    const realPoints = chartData.filter((p) => p.durationMs != null);
    if (realPoints.length < 2) {
      const color =
        realPoints.length === 1 && realPoints[0].status !== "up"
          ? COLOR_DOWN
          : COLOR_UP;
      return [{ offset: 0, color }, { offset: 1, color }];
    }
    const n = realPoints.length;
    const stops: { offset: number; color: string }[] = [];
    const colorFor = (status: string) =>
      status === "up" ? COLOR_UP : COLOR_DOWN;

    stops.push({ offset: 0, color: colorFor(realPoints[0].status) });

    for (let i = 1; i < n; i++) {
      if (realPoints[i].status !== realPoints[i - 1].status) {
        const mid = (i - 0.5) / (n - 1);
        stops.push({
          offset: mid,
          color: colorFor(realPoints[i - 1].status),
        });
        stops.push({ offset: mid, color: colorFor(realPoints[i].status) });
      }
    }

    stops.push({ offset: 1, color: colorFor(realPoints[n - 1].status) });
    return stops;
  }, [chartData]);

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle>Response Times</CardTitle>
        <div className="flex items-center gap-3">
          <label className="flex items-center gap-1.5 text-xs text-muted-foreground cursor-pointer">
            <Switch
              checked={fullRange}
              onCheckedChange={updateFullRange}
              className="scale-75"
            />
            Full range
          </label>
          <div className="flex items-center gap-1">
            {(["hour", "day", "week", "month"] as TimeRange[]).map((range) => (
              <Button
                key={range}
                variant={timeRange === range ? "default" : "outline"}
                size="sm"
                onClick={() => updateTimeRange(range)}
                className="capitalize"
              >
                {range}
              </Button>
            ))}
          </div>
        </div>
      </CardHeader>
      <CardContent>
        {regions.length > 1 && (
          <div className="text-xs text-muted-foreground mb-2">
            Showing all regions ({regions.join(", ")})
          </div>
        )}
        {isLoading ? (
          <Skeleton className="h-[300px] w-full" />
        ) : chartData.length === 0 ? (
          <div className="h-[300px] flex items-center justify-center text-muted-foreground">
            No data available
          </div>
        ) : (
          <ResponsiveContainer width="100%" height={300}>
            <AreaChart data={chartData}>
              <defs>
                <linearGradient
                  id={`strokeGradient-${checkUid}`}
                  x1="0"
                  y1="0"
                  x2="1"
                  y2="0"
                >
                  {gradientStops.map((s, i) => (
                    <stop key={i} offset={s.offset} stopColor={s.color} />
                  ))}
                </linearGradient>
                <linearGradient
                  id={`fillGradient-${checkUid}`}
                  x1="0"
                  y1="0"
                  x2="1"
                  y2="0"
                >
                  {gradientStops.map((s, i) => (
                    <stop
                      key={i}
                      offset={s.offset}
                      stopColor={s.color}
                      stopOpacity={0.15}
                    />
                  ))}
                </linearGradient>
              </defs>
              <CartesianGrid
                strokeDasharray="3 3"
                className="stroke-muted"
              />
              <XAxis
                dataKey="ts"
                type="number"
                scale="time"
                domain={[domainMin, domainMax]}
                ticks={ticks}
                tickFormatter={(v) => adaptiveFormat(v, formatSpanMs)}
                minTickGap={50}
                className="text-xs"
                tick={{ fill: "var(--muted-foreground)" }}
              />
              <YAxis
                tickFormatter={formatMs}
                className="text-xs"
                tick={{ fill: "var(--muted-foreground)" }}
                width={60}
              />
              <Tooltip
                content={({ active, payload }) => {
                  if (!active || !payload?.length) return null;
                  const data = payload[0].payload as ChartPoint;
                  if (data.durationMs == null) return null;
                  return (
                    <div className="rounded-md border bg-popover p-2 text-sm shadow-md">
                      <p className="text-muted-foreground">
                        {format(new Date(data.ts), "MMM d, HH:mm:ss")}
                      </p>
                      <p className="font-medium">
                        {formatMs(data.durationMs)}
                      </p>
                      {data.status && data.status !== "up" && (
                        <p className="text-xs font-medium text-red-500 capitalize">
                          {data.status}
                        </p>
                      )}
                    </div>
                  );
                }}
              />
              {gaps.map((gap, i) => (
                <ReferenceArea
                  key={`gap-${i}`}
                  x1={gap.x1}
                  x2={gap.x2}
                  fill="var(--muted)"
                  fillOpacity={0.3}
                  label={
                    gap.showLabel
                      ? {
                          value: "No data",
                          position: "center",
                          fill: "var(--muted-foreground)",
                          fontSize: 11,
                        }
                      : undefined
                  }
                />
              ))}
              <Area
                type="monotone"
                dataKey="durationMs"
                stroke={`url(#strokeGradient-${checkUid})`}
                fill={`url(#fillGradient-${checkUid})`}
                strokeWidth={2}
                connectNulls={false}
                animationDuration={300}
              />
            </AreaChart>
          </ResponsiveContainer>
        )}
      </CardContent>
    </Card>
  );
}
