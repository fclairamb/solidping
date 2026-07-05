import { useState, useMemo, useRef, useCallback } from "react";
import { useTranslation } from "react-i18next";
import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  ReferenceArea,
  type MouseHandlerDataParam,
} from "recharts";
import { format, subDays, subHours, startOfMinute } from "date-fns";
import { useAllResults, useRegions } from "@/api/hooks";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { PinnedResultBox } from "@/components/checks/pinned-result-box";
import { statusStyle } from "@/lib/status-style";

type TimeRange = "hour" | "day" | "week" | "month";

interface ResponseTimeChartProps {
  org: string;
  checkUid: string;
  refetchInterval?: number;
  // Check period in ms — drives raw-vs-hour tier choice on the "day" range so
  // dense (1-min) checks roll up to ~24 hourly points instead of ~1440 raw.
  periodMs?: number;
  initialPeriod?: TimeRange;
  initialFullRange?: boolean;
  initialRegion?: string;
  onSettingsChange?: (
    period: TimeRange,
    fullRange: boolean,
    region: string | null,
  ) => void;
}

interface ChartPoint {
  ts: number;
  durationMs: number | null;
  status: string;
  uid?: string;
  periodType?: string;
  region?: string;
  durationMinMs?: number;
  durationMaxMs?: number;
  durationAvgMs?: number;
  durationP95Ms?: number;
  totalChecks?: number;
  // Multi-series mode only: each region's own Area uses a distinct dataKey
  // (regionDataKey(slug)) so a chart-level click's activeDataKey identifies
  // which region's line was clicked — recharts synchronizes activeTooltipIndex
  // across series sharing one XAxis, so a plain index lookup (single-series
  // approach) can't disambiguate once there's more than one data array.
  [regionDataKey: `durationMs:${string}`]: number | null | undefined;
}

/** Builds the per-region dataKey name used for multi-series Area/click
 * resolution (see ChartPoint's index signature above). */
function regionDataKey(slug: string): `durationMs:${string}` {
  return `durationMs:${slug}`;
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

/** Shared ms/s duration formatting convention — also used by the Recent
 * Results stats strip so both surfaces render durations identically. */
export function formatMs(value: number): string {
  if (value >= 1000) return `${(value / 1000).toFixed(1)}s`;
  return `${Math.round(value)}ms`;
}

/** Fields fetched for the chart window — extended with stored aggregate stats
 * (durationMinMs/MaxMs/AvgMs/P95Ms, totalChecks) so the Recent Results stats
 * strip can compute tier-aware min/avg/max/p95 from this same dataset without
 * an extra HTTP request. Raw rows simply omit the aggregate-only fields. */
export const CHART_WITH_FIELDS =
  "durationMs,region,durationMinMs,durationMaxMs,durationAvgMs,durationP95Ms,totalChecks";

export interface ChartFetchParams {
  periodStartAfter: string;
  periodType: string;
  with: string;
  size: number;
}

/** Builds the exact `useAllResults` query params for the chart's current
 * window (time range + check period). Exported so the results-list route can
 * issue an identical query (same react-query key) to derive the observed
 * region set and duration stats from the chart's already-fetched window,
 * with zero extra HTTP requests. */
export function chartFetchParams(
  timeRange: TimeRange,
  periodMs: number | undefined,
): ChartFetchParams {
  const periodStartAfter = getStartFor(timeRange);

  // Include raw as a co-tier so the current open bucket (which the aggregator
  // never rolls up until it closes) is always represented. The aggregator
  // deletes source rows after rollup, so raw + hour + day are disjoint in
  // time — no duplicates. detectGaps() already handles tier transitions.
  const denseEnoughForHourly = (periodMs ?? 60_000) < 5 * 60_000;
  const periodType =
    timeRange === "month"
      ? "raw,hour,day"
      : timeRange === "week"
        ? "raw,hour"
        : timeRange === "day"
          ? denseEnoughForHourly
            ? "raw,hour"
            : "raw"
          : "raw";

  return {
    periodStartAfter,
    periodType,
    with: CHART_WITH_FIELDS,
    size: 1000,
  };
}

/** Compute median of a sorted array of numbers */
function median(sorted: number[]): number {
  if (sorted.length === 0) return 0;
  const mid = Math.floor(sorted.length / 2);
  return sorted.length % 2 === 0
    ? (sorted[mid - 1] + sorted[mid]) / 2
    : sorted[mid];
}

/** Detect gaps in data where interval exceeds 5x the per-tier median interval.
 * Transitions between different periodTypes (raw → hour → day → month) are
 * not gaps — the aggregator deletes source rows so the timeline is contiguous
 * across tiers, just at different densities. */
function detectGaps(
  data: ChartPoint[],
  domainMin: number,
  domainMax: number,
): GapRegion[] {
  if (data.length < 2) return [];

  // Per-tier median: only count intervals between same-tier neighbors.
  const intervalsByType: Record<string, number[]> = {};
  for (let i = 1; i < data.length; i++) {
    const prev = data[i - 1];
    const cur = data[i];
    if (prev.periodType !== cur.periodType) continue;
    const key = cur.periodType ?? "";
    (intervalsByType[key] ??= []).push(cur.ts - prev.ts);
  }
  const medianByType: Record<string, number> = {};
  for (const [key, list] of Object.entries(intervalsByType)) {
    medianByType[key] = median([...list].sort((a, b) => a - b));
  }

  const domainSpan = domainMax - domainMin;
  const gaps: GapRegion[] = [];

  for (let i = 1; i < data.length; i++) {
    const prev = data[i - 1];
    const cur = data[i];
    if (prev.periodType !== cur.periodType) continue;
    const tierMedian = medianByType[cur.periodType ?? ""] ?? 0;
    if (tierMedian === 0) continue;
    const interval = cur.ts - prev.ts;
    if (interval > tierMedian * 5) {
      gaps.push({
        x1: prev.ts,
        x2: cur.ts,
        showLabel: domainSpan > 0 && interval / domainSpan > 0.1,
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

/** Runs gap detection + gap-marker insertion on `data`, then (when
 * `fullRange`) wraps the result with domain-boundary null markers so the
 * series always spans the full requested window rather than just its own
 * data's min/max. Shared by the combined series and by each per-region
 * series in multi-series mode so both behave identically. */
function buildSeriesForRange(
  data: ChartPoint[],
  fullRange: boolean,
  rangeStartMs: number,
  rangeEndMs: number,
): { series: ChartPoint[]; gaps: GapRegion[] } {
  if (!fullRange) {
    const tsList = data.map((d) => d.ts);
    const min = tsList.length ? Math.min(...tsList) : 0;
    const max = tsList.length ? Math.max(...tsList) : 0;
    const detectedGaps = detectGaps(data, min, max);
    return { series: insertGapMarkers(data, detectedGaps), gaps: detectedGaps };
  }

  const detectedGaps = detectGaps(data, rangeStartMs, rangeEndMs);
  const dataWithGapMarkers = insertGapMarkers(data, detectedGaps);

  const points: ChartPoint[] = [];
  points.push({ ts: rangeStartMs, durationMs: null, status: "up" });

  if (dataWithGapMarkers.length > 0) {
    const firstReal = dataWithGapMarkers.find((p) => p.durationMs != null);
    if (firstReal && firstReal.ts - rangeStartMs > 1) {
      points.push({ ts: firstReal.ts - 1, durationMs: null, status: "up" });
    }
    points.push(...dataWithGapMarkers);
    const lastReal = [...dataWithGapMarkers]
      .reverse()
      .find((p) => p.durationMs != null);
    if (lastReal && rangeEndMs - lastReal.ts > 1) {
      points.push({ ts: lastReal.ts + 1, durationMs: null, status: "up" });
    }
  }

  points.push({ ts: rangeEndMs, durationMs: null, status: "up" });

  return { series: points, gaps: detectedGaps };
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
  periodMs,
  initialPeriod,
  initialFullRange,
  initialRegion,
  onSettingsChange,
}: ResponseTimeChartProps) {
  const { t } = useTranslation("checks");
  const [timeRange, setTimeRange] = useState<TimeRange>(initialPeriod ?? "day");
  const [fullRange, setFullRange] = useState(initialFullRange ?? false);
  const [selectedRegion, setSelectedRegion] = useState<string | null>(
    initialRegion ?? null,
  );
  const [selectedUid, setSelectedUid] = useState<string | null>(null);
  // react-query dedupes with the page's own useRegions(org) call.
  const { data: regionsData } = useRegions(org);
  const dotPositions = useRef<Record<string, { cx: number; cy: number }>>({});
  const observerRef = useRef<ResizeObserver | null>(null);
  const [chartWidth, setChartWidth] = useState(0);
  const [chartHeight, setChartHeight] = useState(0);

  // Callback ref so the observer attaches whenever the wrapper enters the
  // DOM — including after the loading skeleton clears, which happens on a
  // later render than the initial mount.
  const chartWrapperRef = useCallback((node: HTMLDivElement | null) => {
    observerRef.current?.disconnect();
    observerRef.current = null;
    if (!node) return;
    const rect = node.getBoundingClientRect();
    setChartWidth(rect.width);
    setChartHeight(rect.height);
    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) {
        setChartWidth(entry.contentRect.width);
        setChartHeight(entry.contentRect.height);
      }
    });
    observer.observe(node);
    observerRef.current = observer;
  }, []);

  const updateTimeRange = (range: TimeRange) => {
    setTimeRange(range);
    onSettingsChange?.(range, fullRange, selectedRegion);
  };

  const updateFullRange = (full: boolean) => {
    setFullRange(full);
    onSettingsChange?.(timeRange, full, selectedRegion);
  };

  const updateRegion = (region: string | null) => {
    setSelectedRegion(region);
    onSettingsChange?.(timeRange, fullRange, region);
  };

  const handleDotClick = (uid: string) => {
    if (selectedUid === uid) {
      setSelectedUid(null);
      return;
    }
    setSelectedUid(uid);
  };

  const { periodStartAfter, periodType, with: chartWith, size: chartSize } =
    useMemo(() => chartFetchParams(timeRange, periodMs), [timeRange, periodMs]);

  // Derive a chart-specific refetch floor: 30s for hour/day, 5min for
  // week/month. The user just wants the line to move within a minute or two
  // — the page's fast first-30s window doesn't apply to the graph.
  const baseInterval = periodMs ?? refetchInterval ?? 60_000;
  const chartRefetchInterval =
    timeRange === "week" || timeRange === "month"
      ? Math.max(baseInterval, 5 * 60_000)
      : Math.max(baseInterval, 30_000);

  const { data: results, isLoading } = useAllResults(org, {
    checkUid,
    periodStartAfter,
    periodType,
    with: chartWith,
    size: chartSize,
    refetchInterval: chartRefetchInterval,
  });

  const {
    chartData,
    regions,
    formatSpanMs,
    domainMin,
    domainMax,
    gaps,
    effectiveRegion,
    seriesByRegion,
  } = useMemo(() => {
    const hasData = !!results?.data?.length;

    if (!hasData && !fullRange)
      return {
        chartData: [] as ChartPoint[],
        regions: [] as string[],
        formatSpanMs: 0,
        domainMin: 0,
        domainMax: 0,
        gaps: [] as GapRegion[],
        effectiveRegion: null as string | null,
        seriesByRegion: {} as Record<string, ChartPoint[]>,
      };

    const regionSet = new Set<string>();
    const sorted = [...(results?.data ?? [])].reverse();

    const unfilteredData: ChartPoint[] = sorted
      .filter((r) => r.periodStart)
      .map((r) => {
        if (r.region) regionSet.add(r.region);
        return {
          ts: new Date(r.periodStart!).getTime(),
          durationMs: r.durationMs ?? 0,
          status: r.status ?? "up",
          uid: r.uid,
          periodType: r.periodType,
          region: r.region,
          durationMinMs: r.durationMinMs,
          durationMaxMs: r.durationMaxMs,
          durationAvgMs: r.durationAvgMs,
          durationP95Ms: r.durationP95Ms,
          totalChecks: r.totalChecks,
        };
      });

    // Stale-selection guard: only apply the filter when the selected
    // region is actually present in the unfiltered data. This keeps a
    // stale `?graphRegion=` deep link (regions changed, older data aged
    // out) from silently emptying the chart — it falls back to "All".
    const effectiveRegion =
      selectedRegion && regionSet.has(selectedRegion) ? selectedRegion : null;

    // Filter before gap detection, gradient stops, and domain/tick
    // computation so all of those operate on a single region's steady
    // cadence rather than the interleaved multi-region sawtooth. The chip
    // list (regionSet, and `regions` below) stays derived from the
    // unfiltered set so chips never vanish while a filter is applied.
    const data: ChartPoint[] = effectiveRegion
      ? unfilteredData.filter((d) => d.region === effectiveRegion)
      : unfilteredData;

    // Per-region series for the multi-series "All regions" view: run gap
    // detection/insertion (and, in full-range mode, boundary clamping)
    // independently per region via the same buildSeriesForRange() helper
    // used for the combined series, so one region's outage renders a gap
    // only on that region's own line, never bridged by another region's
    // points. Computed unconditionally (cheap relative to the fetch) —
    // only rendered when effectiveRegion === null && regionSet.size > 1.
    const rangeStartMsForRegions = fullRange
      ? new Date(periodStartAfter).getTime()
      : 0;
    const rangeEndMsForRegions = fullRange
      ? startOfMinute(new Date()).getTime()
      : 0;
    const seriesByRegion: Record<string, ChartPoint[]> = {};
    for (const slug of regionSet) {
      const regionData = unfilteredData.filter((d) => d.region === slug);
      const key = regionDataKey(slug);
      const built = buildSeriesForRange(
        regionData,
        fullRange,
        rangeStartMsForRegions,
        rangeEndMsForRegions,
      ).series;
      // Mirror durationMs (including synthetic gap/boundary nulls inserted by
      // buildSeriesForRange) onto the region-specific key so this region's
      // <Area dataKey={key}> plots real values and breaks on nulls exactly
      // like the combined series does. The click handler then maps a clicked
      // activeDataKey straight back to its region slug.
      seriesByRegion[slug] = built.map((p) => ({ ...p, [key]: p.durationMs }));
    }

    if (fullRange) {
      const rangeStartMs = rangeStartMsForRegions;
      const rangeEndMs = rangeEndMsForRegions;
      const fullSpan = rangeEndMs - rangeStartMs;

      const { series: points, gaps: detectedGaps } = buildSeriesForRange(
        data,
        true,
        rangeStartMs,
        rangeEndMs,
      );

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
        effectiveRegion,
        seriesByRegion,
      };
    }

    // Non-full-range: compute span from data + detect gaps
    const tsList = data.map((d) => d.ts);
    const min = tsList.length ? Math.min(...tsList) : 0;
    const max = tsList.length ? Math.max(...tsList) : 0;
    const span = max - min;

    const { series: dataWithGapMarkers, gaps: detectedGaps } =
      buildSeriesForRange(data, false, 0, 0);

    return {
      chartData: dataWithGapMarkers,
      regions: Array.from(regionSet),
      formatSpanMs: span,
      domainMin: min,
      domainMax: max,
      gaps: detectedGaps,
      effectiveRegion,
      seriesByRegion,
    };
  }, [results, fullRange, periodStartAfter, selectedRegion]);

  const ticks = useMemo(
    () => computeTicks(domainMin, domainMax, chartData),
    [domainMin, domainMax, chartData],
  );

  const regionBySlug = useMemo(() => {
    const map = new Map<string, { emoji: string; name: string }>();
    for (const def of regionsData?.regions ?? []) {
      map.set(def.slug, { emoji: def.emoji, name: def.name });
    }
    return map;
  }, [regionsData]);

  const regionLabel = (slug: string) => {
    const def = regionBySlug.get(slug);
    return def ? `${def.emoji} ${def.name}` : slug;
  };

  // Multi-series "All regions" view: only when no single region is selected
  // AND more than one region is actually observed. Selecting one region (or
  // a single-region check) always takes the existing single-series path
  // below, unchanged.
  const isMultiSeries = effectiveRegion === null && regions.length > 1;

  // Stable per-region stroke color, cycling through the 5-color chart
  // palette in sorted slug order so the same region always gets the same
  // color across renders/reloads regardless of fetch order.
  const sortedRegionsForColor = useMemo(
    () => [...regions].sort(),
    [regions],
  );
  const colorByRegion = useMemo(() => {
    const map = new Map<string, string>();
    sortedRegionsForColor.forEach((slug, i) => {
      map.set(slug, `var(--chart-${(i % 5) + 1})`);
    });
    return map;
  }, [sortedRegionsForColor]);

  // Reverse lookup for click resolution — see handleChartClick below.
  const dataKeyToRegion = useMemo(() => {
    const map = new Map<string, string>();
    for (const slug of regions) map.set(regionDataKey(slug), slug);
    return map;
  }, [regions]);

  // Dot-density threshold (dots only render when count <= 150) applies to
  // the TOTAL point count across every series combined, not per-series —
  // otherwise splitting one dense combined series into several per-region
  // series would make dots reappear on each one individually.
  const totalPointCount = isMultiSeries
    ? Object.values(seriesByRegion).reduce((sum, series) => sum + series.length, 0)
    : chartData.length;
  const dotsEnabled = totalPointCount <= 150;

  const COLOR_UP = "hsl(142, 76%, 36%)";
  const COLOR_DOWN = "hsl(0, 72%, 51%)";

  const gradientStops = useMemo(() => {
    // Non-failing statuses (up, created, running, and the warning/degraded
    // "up, but something to report" states) render neutral green — only hard
    // failures (down/error/timeout/unknown) tint the line red.
    const isNeutralStatus = (status: string) => !statusStyle(status).isDown;
    const realPoints = chartData.filter((p) => p.durationMs != null);
    if (realPoints.length < 2) {
      const color =
        realPoints.length === 1 && !isNeutralStatus(realPoints[0].status)
          ? COLOR_DOWN
          : COLOR_UP;
      return [{ offset: 0, color }, { offset: 1, color }];
    }
    const n = realPoints.length;
    const stops: { offset: number; color: string }[] = [];
    const colorFor = (status: string) =>
      isNeutralStatus(status) ? COLOR_UP : COLOR_DOWN;

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

  // Recharts v3 routes all pointer events through a single top-level surface,
  // so per-dot onClick handlers never fire. Handle clicks at the chart level
  // and resolve the active point via activeTooltipIndex into chartData.
  //
  // Multi-series mode can't reuse that index lookup: each region's Area has
  // its own `data` array (different length once gap markers are inserted
  // per-region), so a single activeTooltipIndex doesn't address any one of
  // them safely. state.activeDataKey looks like the obvious way to name which
  // line was clicked, but recharts' chart-level click middleware
  // (mouseEventsMiddleware.js, axisInteraction path) hardcodes
  // activeDataKey: undefined for every chart-wide click — it's only ever
  // populated by an item's own hover/click listeners, which multi-series
  // Area doesn't wire up here. So instead: activeLabel (the shared x-axis ts)
  // narrows to the set of regions with a real point at that ts, and
  // activeCoordinate.y (pixel space) picks whichever of those candidates'
  // cached dot position (dotPositions, populated by every series' own
  // dot/activeDot renderer) sits closest to the click — i.e. the line the
  // user actually clicked on screen.
  const handleChartClick = (state: MouseHandlerDataParam) => {
    if (isMultiSeries) {
      const label = state?.activeLabel;
      const ts = typeof label === "number" ? label : Number(label);
      const clickY = state?.activeCoordinate?.y;
      let best: { uid: string; distance: number } | undefined;
      if (Number.isFinite(ts) && typeof clickY === "number") {
        for (const slug of regions) {
          const point = seriesByRegion[slug]?.find((p) => p.ts === ts);
          if (!point?.uid) continue;
          const cy = dotPositions.current[point.uid]?.cy;
          if (cy == null) continue;
          const distance = Math.abs(cy - clickY);
          if (!best || distance < best.distance) {
            best = { uid: point.uid, distance };
          }
        }
      }
      if (best) {
        handleDotClick(best.uid);
      } else {
        setSelectedUid(null);
      }
      return;
    }

    // activeTooltipIndex is `number | string | null` across recharts versions.
    const raw = state?.activeTooltipIndex;
    const idx = typeof raw === "number" ? raw : raw != null ? Number(raw) : NaN;
    const point = Number.isInteger(idx) ? chartData[idx] : undefined;
    if (point?.uid) {
      handleDotClick(point.uid);
    } else {
      setSelectedUid(null);
    }
  };

  return (
    <Card>
      <CardHeader className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between space-y-0 pb-2">
        <CardTitle>{t("detail.chart.title")}</CardTitle>
        <div className="flex flex-wrap items-center gap-2 sm:gap-3">
          <label className="flex items-center gap-1.5 text-xs text-muted-foreground cursor-pointer">
            <Switch
              checked={fullRange}
              onCheckedChange={updateFullRange}
              className="scale-75"
            />
            <span className="hidden sm:inline">{t("detail.chart.fullRange")}</span>
          </label>
          <div className="flex items-center gap-1">
            {(["hour", "day", "week", "month"] as TimeRange[]).map((range) => (
              <Button
                key={range}
                variant={timeRange === range ? "default" : "outline"}
                size="sm"
                onClick={() => updateTimeRange(range)}
                className="px-2 text-xs sm:px-3 sm:text-sm"
              >
                <span className="sm:hidden">
                  {t(`detail.chart.range${range.charAt(0).toUpperCase() + range.slice(1)}Short`)}
                </span>
                <span className="hidden sm:inline">
                  {t(`detail.chart.range${range.charAt(0).toUpperCase() + range.slice(1)}`)}
                </span>
              </Button>
            ))}
          </div>
        </div>
      </CardHeader>
      <CardContent>
        {regions.length > 1 && (
          <div
            className="flex flex-wrap items-center gap-1 mb-2"
            data-testid="response-time-chart-region-filter"
          >
            <Button
              variant={effectiveRegion === null ? "default" : "outline"}
              size="sm"
              onClick={() => updateRegion(null)}
              className="px-2 text-xs"
            >
              {t("detail.chart.allRegions")}
            </Button>
            {regions.map((slug) => (
              <Button
                key={slug}
                variant={effectiveRegion === slug ? "default" : "outline"}
                size="sm"
                onClick={() => updateRegion(slug)}
                className="px-2 text-xs gap-1.5"
                data-testid={`response-time-chart-region-chip-${slug}`}
              >
                {isMultiSeries && (
                  <span
                    className="inline-block h-2.5 w-2.5 shrink-0 rounded-sm"
                    style={{ backgroundColor: colorByRegion.get(slug) }}
                    aria-hidden="true"
                    data-testid={`response-time-chart-region-swatch-${slug}`}
                  />
                )}
                {regionLabel(slug)}
              </Button>
            ))}
          </div>
        )}
        {isLoading ? (
          <Skeleton className="h-[300px] w-full" />
        ) : chartData.length === 0 ? (
          <div className="h-[300px] flex items-center justify-center text-muted-foreground">
            {t("detail.chart.noDataAvailable")}
          </div>
        ) : (
          <div ref={chartWrapperRef} className="relative">
          <ResponsiveContainer width="100%" height={300}>
            <AreaChart data={isMultiSeries ? undefined : chartData} onClick={handleChartClick}>
              {!isMultiSeries && (
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
              )}
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

                  if (isMultiSeries) {
                    // payload has one entry per region series at this x
                    // position; most are null (sparse per-region data) —
                    // find the one with a real value.
                    const entry = payload.find((p) => {
                      const key =
                        typeof p.dataKey === "string" ? p.dataKey : undefined;
                      const point = p.payload as ChartPoint | undefined;
                      return (
                        key && point && point[key as `durationMs:${string}`] != null
                      );
                    });
                    if (!entry) return null;
                    const data = entry.payload as ChartPoint;
                    if (data.durationMs == null) return null;
                    if (data.uid && data.uid === selectedUid) return null;
                    const key =
                      typeof entry.dataKey === "string" ? entry.dataKey : undefined;
                    const slug = key ? dataKeyToRegion.get(key) : undefined;
                    return (
                      <div className="rounded-md border bg-popover p-2 text-sm shadow-md">
                        {slug && (
                          <p
                            className="flex items-center gap-1.5 font-medium"
                          >
                            <span
                              className="inline-block h-2 w-2 shrink-0 rounded-sm"
                              style={{ backgroundColor: colorByRegion.get(slug) }}
                              aria-hidden="true"
                            />
                            {regionLabel(slug)}
                          </p>
                        )}
                        <p className="text-muted-foreground">
                          {format(new Date(data.ts), "MMM d, HH:mm:ss")}
                        </p>
                        <p className="font-medium">{formatMs(data.durationMs)}</p>
                        {data.status && data.status !== "up" && (
                          <p className="text-xs font-medium text-red-500 capitalize">
                            {data.status}
                          </p>
                        )}
                        {data.uid && (
                          <p className="text-xs text-muted-foreground mt-1">
                            {t("detail.chart.tooltipClickPoint")}
                          </p>
                        )}
                      </div>
                    );
                  }

                  const data = payload[0].payload as ChartPoint;
                  if (data.durationMs == null) return null;
                  if (data.uid && data.uid === selectedUid) return null;
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
                      {data.uid && (
                        <p className="text-xs text-muted-foreground mt-1">
                          {t("detail.chart.tooltipClickPoint")}
                        </p>
                      )}
                    </div>
                  );
                }}
              />
              {/* Shaded "no data" gap bands reflect the combined-series gap
                  detection, which mixes all regions together — showing them
                  in multi-series mode would misleadingly shade a region's
                  window even when another region has data there. The
                  per-series null markers already break each line at its own
                  gaps (criterion: gap renders only on that region's line);
                  the shared band is single-series-only. */}
              {!isMultiSeries &&
                gaps.map((gap, i) => (
                  <ReferenceArea
                    key={`gap-${i}`}
                    x1={gap.x1}
                    x2={gap.x2}
                    fill="var(--muted)"
                    fillOpacity={0.3}
                    label={
                      gap.showLabel
                        ? {
                            value: t("detail.chart.noData"),
                            position: "center",
                            fill: "var(--muted-foreground)",
                            fontSize: 11,
                          }
                        : undefined
                    }
                  />
                ))}
              {!isMultiSeries && (
              <Area
                type="monotone"
                dataKey="durationMs"
                stroke={`url(#strokeGradient-${checkUid})`}
                fill={`url(#fillGradient-${checkUid})`}
                strokeWidth={2}
                connectNulls={false}
                isAnimationActive={false}
                dot={
                  // Dense data → skip per-point circles entirely. activeDot
                  // still renders the hover/selected dot and caches the anchor
                  // so the pinned-result box can find it.
                  !dotsEnabled
                    ? false
                    : (props) => {
                        const dotProps = props as {
                          cx?: number;
                          cy?: number;
                          payload?: ChartPoint;
                          key?: React.Key | null;
                        };
                        const { cx, cy, payload, key } = dotProps;
                        const reactKey =
                          key == null ? undefined : (key as React.Key);
                        if (cx == null || cy == null || !payload?.uid) {
                          return <g key={reactKey} />;
                        }
                        const uid = payload.uid;
                        // Cache the dot's coordinates for the pinned-box anchor.
                        // Mutating a ref outside the React commit phase is safe —
                        // it doesn't trigger a re-render.
                        dotPositions.current[uid] = { cx, cy };
                        const fill =
                          payload.status === "unknown" ||
                          statusStyle(payload.status).isDown
                            ? COLOR_DOWN
                            : COLOR_UP;
                        const isSelected = selectedUid === uid;
                        return (
                          <circle
                            key={reactKey}
                            cx={cx}
                            cy={cy}
                            r={isSelected ? 5 : 3.5}
                            fill={fill}
                            stroke={isSelected ? "var(--primary)" : undefined}
                            strokeWidth={isSelected ? 2 : 0}
                            style={{ cursor: "pointer" }}
                          >
                            <title>
                              {isSelected
                                ? t("detail.chart.dotClickToClose")
                                : t("detail.chart.dotClickForDetails")}
                            </title>
                          </circle>
                        );
                      }
                }
                activeDot={(props) => {
                  const dotProps = props as {
                    cx?: number;
                    cy?: number;
                    payload?: ChartPoint;
                    key?: React.Key | null;
                  };
                  const { cx, cy, payload, key } = dotProps;
                  const reactKey =
                    key == null ? undefined : (key as React.Key);
                  if (cx == null || cy == null || !payload?.uid) {
                    return <g key={reactKey} />;
                  }
                  const uid = payload.uid;
                  // Always cache the active-dot anchor so the pinned-result box
                  // works even when per-point dots are off.
                  dotPositions.current[uid] = { cx, cy };
                  const fill =
                    payload.status === "down" ||
                    payload.status === "unknown"
                      ? COLOR_DOWN
                      : COLOR_UP;
                  return (
                    <circle
                      key={reactKey}
                      cx={cx}
                      cy={cy}
                      r={5}
                      fill={fill}
                      style={{ cursor: "pointer" }}
                    />
                  );
                }}
              />
              )}
              {isMultiSeries &&
                regions.map((slug) => {
                  const seriesData = seriesByRegion[slug] ?? [];
                  const key = regionDataKey(slug);
                  const color = colorByRegion.get(slug) ?? COLOR_UP;
                  return (
                    <Area
                      key={slug}
                      data={seriesData}
                      type="monotone"
                      dataKey={key}
                      stroke={color}
                      fill={color}
                      fillOpacity={0.08}
                      strokeWidth={2}
                      connectNulls={false}
                      isAnimationActive={false}
                      dot={
                        // Same combined-total dot-density threshold as
                        // single-series mode (totalPointCount / dotsEnabled).
                        !dotsEnabled
                          ? false
                          : (props) => {
                              const dotProps = props as {
                                cx?: number;
                                cy?: number;
                                payload?: ChartPoint;
                                key?: React.Key | null;
                              };
                              const { cx, cy, payload, key: reactKeyRaw } = dotProps;
                              const reactKey =
                                reactKeyRaw == null ? undefined : (reactKeyRaw as React.Key);
                              if (cx == null || cy == null || !payload?.uid) {
                                return <g key={reactKey} />;
                              }
                              const uid = payload.uid;
                              dotPositions.current[uid] = { cx, cy };
                              // Down results stay visible as red dots on
                              // their line even though the line itself uses
                              // the region color, not the status gradient.
                              const fill =
                                payload.status === "unknown" ||
                                statusStyle(payload.status).isDown
                                  ? COLOR_DOWN
                                  : color;
                              const isSelected = selectedUid === uid;
                              return (
                                <circle
                                  key={reactKey}
                                  cx={cx}
                                  cy={cy}
                                  r={isSelected ? 5 : 3.5}
                                  fill={fill}
                                  stroke={isSelected ? "var(--primary)" : undefined}
                                  strokeWidth={isSelected ? 2 : 0}
                                  style={{ cursor: "pointer" }}
                                >
                                  <title>
                                    {isSelected
                                      ? t("detail.chart.dotClickToClose")
                                      : t("detail.chart.dotClickForDetails")}
                                  </title>
                                </circle>
                              );
                            }
                      }
                      activeDot={(props) => {
                        const dotProps = props as {
                          cx?: number;
                          cy?: number;
                          payload?: ChartPoint;
                          key?: React.Key | null;
                        };
                        const { cx, cy, payload, key: reactKeyRaw } = dotProps;
                        const reactKey =
                          reactKeyRaw == null ? undefined : (reactKeyRaw as React.Key);
                        if (cx == null || cy == null || !payload?.uid) {
                          return <g key={reactKey} />;
                        }
                        const uid = payload.uid;
                        dotPositions.current[uid] = { cx, cy };
                        const fill =
                          payload.status === "down" || payload.status === "unknown"
                            ? COLOR_DOWN
                            : color;
                        return (
                          <circle
                            key={reactKey}
                            cx={cx}
                            cy={cy}
                            r={5}
                            fill={fill}
                            style={{ cursor: "pointer" }}
                          />
                        );
                      }}
                    />
                  );
                })}
            </AreaChart>
          </ResponsiveContainer>
          {selectedUid && (
            <PinnedResultBox
              org={org}
              checkUid={checkUid}
              resultUid={selectedUid}
              anchor={dotPositions.current[selectedUid]}
              width={chartWidth}
              height={chartHeight}
              onClose={() => setSelectedUid(null)}
            />
          )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
