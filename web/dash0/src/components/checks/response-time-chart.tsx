import { useState, useMemo, useRef, useCallback, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { ZoomOut } from "lucide-react";
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
import { format, startOfMinute } from "date-fns";
import {
  useChartWindowResults,
  useCheckAvailabilityBuckets,
  useRegions,
} from "@/api/hooks";
import {
  chartWindowBounds,
  type ChartTierFetch,
  type TimeRange,
  type ZoomWindow,
} from "@/lib/chart-window";
import {
  CHART_PLOT_INSET_LEFT,
  CHART_PLOT_INSET_RIGHT,
  shouldRenderStrip,
  stripBucketSeconds,
} from "@/lib/availability-strip";
import {
  availabilityDotClass,
  formatAvailabilityPct,
} from "@/lib/availability-status";
import { AvailabilityStrip } from "@/components/ui/availability-strip";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { PinnedResultBox } from "@/components/checks/pinned-result-box";
import { StatusBadge } from "@/components/shared/status-badge";
import { statusStyle } from "@/lib/status-style";
import { regionDisplayLabel, sortRegionSlugs } from "@/lib/region-label";
import { cn } from "@/lib/utils";

// The window/tier plan lives in @/lib/chart-window rather than here, so
// @/api/hooks can build the same plan without importing a component. Only the
// types are re-exported (free at runtime); import the functions from the lib.
export type { ChartTierFetch, TimeRange, ZoomWindow };

interface ResponseTimeChartProps {
  org: string;
  checkUid: string;
  refetchInterval?: number;
  // Check period in ms — drives raw-vs-hour tier choice on the "day" range so
  // dense (1-min) checks roll up to ~24 hourly points instead of ~1440 raw.
  periodMs?: number;
  initialPeriod?: TimeRange;
  initialFullRange?: boolean;
  // Controlled by the URL (spec 2026-08-13-02): the page owns the region
  // value and passes it straight through, so an external region change
  // (Recent Results filter, back/forward, in-app link) re-renders the chart
  // in place instead of requiring a remount. No local seeding — see
  // effectiveRegion below for the chart's own stale-slug guard.
  region?: string;
  onRegionChange?: (region?: string) => void;
  onSettingsChange?: (period: TimeRange, fullRange: boolean) => void;
  // Zoom window (X/time axis only), driven by the URL. Absent → full default
  // range. When set (from < to) the results fetch requests only this window.
  zoomFrom?: number;
  zoomTo?: number;
  // Called on drag-release with the selected window, or with (undefined,
  // undefined) to reset the zoom.
  onZoomChange?: (from?: number, to?: number) => void;
  // Currently-highlighted result (its pinned detail box), driven by the URL so
  // a shared link reproduces the selection. Controlled — no local fallback.
  selectedUid?: string;
  onSelectChange?: (uid?: string) => void;
}

export interface ChartPoint {
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
 * across tiers, just at different densities.
 *
 * Exported for ./chart-seam.test.ts: since spec 2026-08-22-07 the rollup→raw
 * transition is the NORMAL case on every wide range rather than an edge case,
 * so the "a tier change is not a gap" rule has to be pinned rather than
 * assumed. */
export function detectGaps(
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
function insertGapMarkers(data: ChartPoint[], gaps: GapRegion[]): ChartPoint[] {
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

interface GradientStop {
  offset: number;
  color: string;
}

/** Non-failing statuses (up, created, running, and the warning/degraded "up,
 * but something to report" states) render neutral — only hard failures
 * (down/error/timeout/unknown) go red. Shared by the gradient-stop builder
 * below and the always-render-dot predicate so line color and dot color
 * agree on what counts as "failing". */
function isFailingStatus(status: string): boolean {
  return status === "unknown" || statusStyle(status).isDown;
}

/** Returns true when a chart point represents a failing result — used to
 * exempt failure dots from the density threshold (dotsEnabled). */
function isDownPoint(payload: ChartPoint | undefined): boolean {
  return !!payload && isFailingStatus(payload.status);
}

/** Fill color for one chart dot — regular or active, single- or
 * multi-series. Routes through isFailingStatus() so a hovered/pinned point
 * never disagrees with the line gradient about what counts as failing
 * (exported so tests can assert on the exact function the renderers call,
 * not a re-implementation of it). */
export function dotFillColor(
  status: string,
  downColor: string,
  neutralColor: string,
): string {
  return isFailingStatus(status) ? downColor : neutralColor;
}

/** Builds per-status gradient stops for one series' real (non-null) points:
 * `neutralColor` everywhere except failing stretches, which use
 * `downColor`. Stop offsets are index fractions over the real points
 * ((i - 0.5) / (n - 1)) — this assumes roughly even x-spacing, the same
 * assumption the single-series chart has always made (including across
 * mixed raw/hour tiers; fixing x-linearity for unevenly spaced points is out
 * of scope). Shared by the single-series gradient and, per region, the
 * multi-series gradients. */
function buildGradientStops(
  points: ChartPoint[],
  neutralColor: string,
  downColor: string,
): GradientStop[] {
  const realPoints = points.filter((p) => p.durationMs != null);
  const colorFor = (status: string) =>
    isFailingStatus(status) ? downColor : neutralColor;

  if (realPoints.length < 2) {
    const color =
      realPoints.length === 1 ? colorFor(realPoints[0].status) : neutralColor;
    return [
      { offset: 0, color },
      { offset: 1, color },
    ];
  }

  const n = realPoints.length;
  const stops: GradientStop[] = [];
  stops.push({ offset: 0, color: colorFor(realPoints[0].status) });

  for (let i = 1; i < n; i++) {
    if (realPoints[i].status !== realPoints[i - 1].status) {
      const mid = (i - 0.5) / (n - 1);
      stops.push({ offset: mid, color: colorFor(realPoints[i - 1].status) });
      stops.push({ offset: mid, color: colorFor(realPoints[i].status) });
    }
  }

  stops.push({ offset: 1, color: colorFor(realPoints[n - 1].status) });
  return stops;
}

export function ResponseTimeChart({
  org,
  checkUid,
  refetchInterval,
  periodMs,
  initialPeriod,
  initialFullRange,
  region,
  onRegionChange,
  onSettingsChange,
  zoomFrom,
  zoomTo,
  onZoomChange,
  selectedUid,
  onSelectChange,
}: ResponseTimeChartProps) {
  const { t } = useTranslation("checks");
  const [timeRange, setTimeRange] = useState<TimeRange>(initialPeriod ?? "day");
  const [fullRange, setFullRange] = useState(initialFullRange ?? false);
  // A zoom is active only for a well-formed forward window.
  const zoom: ZoomWindow | undefined =
    zoomFrom != null && zoomTo != null && zoomTo > zoomFrom
      ? { from: zoomFrom, to: zoomTo }
      : undefined;
  // In-progress drag selection (recharts x-axis ts bounds). `dragStartRef`
  // holds the mouse/touch-down anchor; `didZoomRef` suppresses the trailing
  // chart onClick so a completed drag never also toggles a point selection.
  const [refAreaLeft, setRefAreaLeft] = useState<number | null>(null);
  const [refAreaRight, setRefAreaRight] = useState<number | null>(null);
  // Refs mirror the drag bounds so the mouseup/touchend handler reads the
  // latest values directly, never a stale render closure (the state copies
  // exist only to render the in-progress <ReferenceArea>).
  const dragStartRef = useRef<number | null>(null);
  const dragEndRef = useRef<number | null>(null);
  const didZoomRef = useRef(false);
  // Bumped once the selected point's dot anchor is cached, so a cold deep-link
  // (?graphSelected=…) re-renders to position the pinned box correctly.
  const [, bumpAnchorTick] = useState(0);
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
    onSettingsChange?.(range, fullRange);
  };

  const updateFullRange = (full: boolean) => {
    setFullRange(full);
    onSettingsChange?.(timeRange, full);
  };

  const updateRegion = (nextRegion?: string) => {
    onRegionChange?.(nextRegion);
  };

  const handleDotClick = (uid: string) => {
    // Controlled selection: toggle via the URL-backed callback.
    onSelectChange?.(selectedUid === uid ? undefined : uid);
  };

  const resetZoom = () => onZoomChange?.(undefined, undefined);

  // The window's own start — still the full requested range, independent of
  // where the raw pass now begins — so the full-range boundary fallback below
  // clamps the x-domain to what the user asked for, not to the seam.
  const { periodStartAfter } = useMemo(
    () => chartWindowBounds(timeRange, zoom),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- zoom is derived from zoomFrom/zoomTo
    [timeRange, zoomFrom, zoomTo],
  );

  // Derive a chart-specific refetch floor: 30s for hour/day, 5min for
  // week/month. The user just wants the line to move within a minute or two
  // — the page's fast first-30s window doesn't apply to the graph. This now
  // paces the RAW pass only; rollups refresh at their bucket width, since a
  // closed bucket cannot change (spec 2026-08-22-07 §3).
  const baseInterval = periodMs ?? refetchInterval ?? 60_000;
  const chartRefetchInterval =
    timeRange === "week" || timeRange === "month"
      ? Math.max(baseInterval, 5 * 60_000)
      : Math.max(baseInterval, 30_000);

  // Two passes: rollups over the whole window first (the chart is interactive
  // from those alone), then raw over the seam between the newest rollup bucket
  // and now. Never one query naming both sides of the raw/rollup index split —
  // that matches neither partial index and scans the whole table
  // (spec 2026-08-22-04). The results route calls this hook with the same
  // arguments, so its region set and duration stats are cache hits.
  const {
    data: results,
    isLoading,
    rawError,
    isEmptyPending,
  } = useChartWindowResults(
    org,
    checkUid,
    { timeRange, periodMs, zoom },
    { rawRefetchInterval: chartRefetchInterval },
  );

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

    // A zoom pins the x-domain to the selected window (like full-range does for
    // the default window), so boundary-clamp and render even with no data in
    // that window — the user still sees the window and the Reset control.
    const effectiveFullRange = fullRange || zoom != null;

    if (!hasData && !effectiveFullRange)
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
    // stale `?region=` deep link (regions changed, older data aged
    // out) from silently emptying the chart — it falls back to "All".
    const effectiveRegion = region && regionSet.has(region) ? region : null;

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
    // When zoomed, the window is exactly [zoom.from, zoom.to]; otherwise the
    // full default range is [range start, now]. periodStartAfter already equals
    // zoom.from's ISO when zoomed, but zoom.to (not `now`) bounds the end.
    const rangeStartMsForRegions = effectiveFullRange
      ? zoom
        ? zoom.from
        : new Date(periodStartAfter).getTime()
      : 0;
    const rangeEndMsForRegions = effectiveFullRange
      ? zoom
        ? zoom.to
        : startOfMinute(new Date()).getTime()
      : 0;
    const seriesByRegion: Record<string, ChartPoint[]> = {};
    for (const slug of regionSet) {
      const regionData = unfilteredData.filter((d) => d.region === slug);
      const key = regionDataKey(slug);
      const built = buildSeriesForRange(
        regionData,
        effectiveFullRange,
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

    if (effectiveFullRange) {
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
        regions: sortRegionSlugs(regionsData?.regions, Array.from(regionSet)),
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
      regions: sortRegionSlugs(regionsData?.regions, Array.from(regionSet)),
      formatSpanMs: span,
      domainMin: min,
      domainMax: max,
      gaps: detectedGaps,
      effectiveRegion,
      seriesByRegion,
    };
    // zoomFrom/zoomTo drive the derived `zoom` window used above.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    results,
    fullRange,
    periodStartAfter,
    region,
    zoomFrom,
    zoomTo,
    regionsData,
  ]);

  const ticks = useMemo(
    () => computeTicks(domainMin, domainMax, chartData),
    [domainMin, domainMax, chartData],
  );

  // The window the availability strip describes. Deliberately derived from the
  // SAME inputs as the chart's own fetch window (periodStartAfter + zoom), so the
  // strip, the chart and the region filter can never describe different spans —
  // which is the exact complaint this spec exists to fix.
  //
  // `to` is resolved inside this memo rather than read per render: it must be
  // stable, or the react-query key would churn every render and refetch forever.
  // It refreshes on the same trigger as periodStartAfter (a range or zoom
  // change), exactly like the chart's own window.
  const stripWindow = useMemo(() => {
    if (zoomFrom != null && zoomTo != null && zoomTo > zoomFrom) {
      return {
        from: new Date(zoomFrom).toISOString(),
        to: new Date(zoomTo).toISOString(),
        spanMs: zoomTo - zoomFrom,
      };
    }

    const to = startOfMinute(new Date()).toISOString();

    return {
      from: periodStartAfter,
      to,
      spanMs: Date.parse(to) - Date.parse(periodStartAfter),
    };
  }, [periodStartAfter, zoomFrom, zoomTo]);

  const stripBucket = stripBucketSeconds(
    timeRange,
    zoom ? stripWindow.spanMs : undefined,
  );
  const stripVisible = shouldRenderStrip(stripWindow.spanMs);

  // One request answers both surfaces: `data` is the cell series and `window` is
  // the exact [from, to) fold behind the header figure. Bucket widths are the
  // spec's, not the server's guess — see @/lib/availability-strip.
  const { data: availability, isLoading: availabilityLoading } =
    useCheckAvailabilityBuckets(
      org,
      checkUid,
      {
        from: stripWindow.from,
        to: stripWindow.to,
        // Below the 3h floor there is no strip, so let the server pick the width
        // and use the response only for the header figure.
        bucketSeconds: stripVisible ? stripBucket : undefined,
        region: effectiveRegion ?? undefined,
      },
      { refetchInterval: chartRefetchInterval },
    );

  const windowPctLabel = formatAvailabilityPct(
    availability?.window.availabilityPct,
  );

  const regionLabel = (slug: string) =>
    regionDisplayLabel(regionsData?.regions, slug);

  // Multi-series "All regions" view: only when no single region is selected
  // AND more than one region is actually observed. Selecting one region (or
  // a single-region check) always takes the existing single-series path
  // below, unchanged.
  const isMultiSeries = effectiveRegion === null && regions.length > 1;

  // Stable per-region stroke color, cycling through the 5-color chart
  // palette in sorted slug order so the same region always gets the same
  // color across renders/reloads regardless of fetch order.
  const sortedRegionsForColor = useMemo(() => [...regions].sort(), [regions]);
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
    ? Object.values(seriesByRegion).reduce(
        (sum, series) => sum + series.length,
        0,
      )
    : chartData.length;
  const dotsEnabled = totalPointCount <= 150;

  const COLOR_UP = "hsl(142, 76%, 36%)";
  const COLOR_DOWN = "hsl(0, 72%, 51%)";

  const gradientStops = useMemo(
    () => buildGradientStops(chartData, COLOR_UP, COLOR_DOWN),
    [chartData],
  );

  // Multi-series mode: one gradient's worth of stops per region, using that
  // region's own palette color as the neutral base instead of the flat
  // COLOR_UP single-series uses — this is what lets a region's line keep its
  // identity color while still turning red over a failing stretch.
  const regionGradientStops = useMemo(() => {
    const map: Record<string, GradientStop[]> = {};
    for (const slug of regions) {
      const neutralColor = colorByRegion.get(slug) ?? COLOR_UP;
      map[slug] = buildGradientStops(
        seriesByRegion[slug] ?? [],
        neutralColor,
        COLOR_DOWN,
      );
    }
    return map;
  }, [regions, seriesByRegion, colorByRegion]);

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
    // A drag-to-zoom just finished: recharts still fires this trailing click —
    // swallow it once so the zoom gesture never also toggles a point selection.
    if (didZoomRef.current) {
      didZoomRef.current = false;
      return;
    }
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
        onSelectChange?.(undefined);
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
      onSelectChange?.(undefined);
    }
  };

  // ---- Drag-to-select X (time) zoom -------------------------------------
  // recharts delivers MouseHandlerDataParam (with `activeLabel` = the x-axis ts)
  // to BOTH the mouse and the touch handlers, so one implementation covers
  // desktop drag and mobile touch-drag alike.
  const activeTs = (state: MouseHandlerDataParam): number | null => {
    const label = state?.activeLabel;
    const ts = typeof label === "number" ? label : Number(label);
    return Number.isFinite(ts) ? ts : null;
  };

  const handleSelectStart = (state: MouseHandlerDataParam) => {
    didZoomRef.current = false;
    const ts = activeTs(state);
    if (ts == null) return;
    dragStartRef.current = ts;
    dragEndRef.current = null;
    setRefAreaLeft(ts);
    setRefAreaRight(null);
  };

  const handleSelectMove = (state: MouseHandlerDataParam) => {
    if (dragStartRef.current == null) return;
    const ts = activeTs(state);
    if (ts == null) return;
    dragEndRef.current = ts;
    setRefAreaRight(ts);
  };

  const handleSelectEnd = () => {
    const start = dragStartRef.current;
    const end = dragEndRef.current;
    dragStartRef.current = null;
    dragEndRef.current = null;
    setRefAreaLeft(null);
    setRefAreaRight(null);
    if (start == null || end == null) return;
    const from = Math.min(start, end);
    const to = Math.max(start, end);
    // Ignore a jittery near-zero drag (treat as a click): require the window to
    // span at least 1% of the currently-visible domain (floor 1s).
    const domainSpan = domainMax - domainMin;
    const minSpan = Math.max(domainSpan * 0.01, 1000);
    if (to - from < minSpan) return;
    didZoomRef.current = true;
    onZoomChange?.(from, to);
  };

  // Cold deep-link (?graphSelected=…): the pinned box reads the dot anchor from
  // dotPositions, which only fills in while the dots render. Bump a tick once
  // the anchor exists so the box re-renders into position. Guarded on the
  // anchor's presence so this settles in one extra render (no loop).
  useEffect(() => {
    if (selectedUid && dotPositions.current[selectedUid]) {
      bumpAnchorTick((n) => n + 1);
    }
  }, [selectedUid, chartData, chartWidth, chartHeight]);

  const zoomed = zoom != null;

  return (
    <Card>
      <CardHeader className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between space-y-0 pb-2">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <CardTitle>{t("detail.chart.title")}</CardTitle>
          {/* Window availability lives in the HEADER, not in the strip area.
              Below the 3h floor it is the ONLY availability figure on the chart
              (a single full-width cell reads as a broken 24-cell strip), and
              keeping it here means it does not reflow when a drag-zoom crosses
              that boundary. */}
          <span
            className="flex items-center gap-1.5 text-xs text-muted-foreground tabular-nums"
            data-testid="response-time-chart-window-availability"
            data-status={availability?.window.status ?? "loading"}
          >
            <span
              className={cn(
                "inline-block h-2 w-2 rounded-full",
                availabilityDotClass(availability?.window.status ?? "noData"),
              )}
              aria-hidden="true"
            />
            {availabilityLoading && !availability
              ? t("detail.availabilityStrip.loading")
              : t("detail.availabilityStrip.windowAvailability", {
                  pct: windowPctLabel ?? t("detail.availabilityStrip.noData"),
                })}
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-2 sm:gap-3">
          {zoomed && (
            <Button
              variant="outline"
              size="sm"
              onClick={resetZoom}
              className="gap-1 px-2 text-xs"
              data-testid="response-time-chart-reset-zoom"
            >
              <ZoomOut className="h-3.5 w-3.5" />
              <span className="hidden sm:inline">
                {t("detail.chart.resetZoom")}
              </span>
            </Button>
          )}
          <label className="flex items-center gap-1.5 text-xs text-muted-foreground cursor-pointer">
            <Switch
              checked={fullRange}
              onCheckedChange={updateFullRange}
              className="scale-75"
              data-testid="response-time-chart-full-range-toggle"
            />
            <span className="hidden sm:inline">
              {t("detail.chart.fullRange")}
            </span>
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
                  {t(
                    `detail.chart.range${range.charAt(0).toUpperCase() + range.slice(1)}Short`,
                  )}
                </span>
                <span className="hidden sm:inline">
                  {t(
                    `detail.chart.range${range.charAt(0).toUpperCase() + range.slice(1)}`,
                  )}
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
              onClick={() => updateRegion(undefined)}
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
        {/* Pass 2 (raw / the chart's right-hand edge) failed. Say so without
            discarding the pass-1 render: a month of rollups is still a usable
            chart, and blanking it would be a strictly worse answer. */}
        {!isLoading && rawError != null && (
          <p
            className="mb-2 text-xs text-muted-foreground"
            data-testid="response-time-chart-raw-error"
          >
            {t("detail.chart.rawUnavailable")}
          </p>
        )}
        {isLoading || (chartData.length === 0 && isEmptyPending) ? (
          <Skeleton className="h-[300px] w-full" />
        ) : chartData.length === 0 ? (
          <div
            className="h-[300px] flex items-center justify-center text-muted-foreground"
            data-testid="response-time-chart-no-data"
          >
            {t("detail.chart.noDataAvailable")}
          </div>
        ) : (
          <div
            ref={chartWrapperRef}
            className="relative select-none"
            data-testid="response-time-chart-wrapper"
            // Double-click anywhere on the chart resets an active zoom.
            onDoubleClick={() => {
              if (zoomed) resetZoom();
            }}
          >
            <ResponsiveContainer width="100%" height={300}>
              <AreaChart
                data={isMultiSeries ? undefined : chartData}
                onClick={handleChartClick}
                onMouseDown={handleSelectStart}
                onMouseMove={handleSelectMove}
                onMouseUp={handleSelectEnd}
                onMouseLeave={handleSelectEnd}
                onTouchStart={handleSelectStart}
                onTouchMove={handleSelectMove}
                onTouchEnd={handleSelectEnd}
              >
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
                {isMultiSeries && (
                  <defs>
                    {regions.map((slug) => (
                      <linearGradient
                        key={slug}
                        id={`rt-region-${slug}`}
                        x1="0"
                        y1="0"
                        x2="1"
                        y2="0"
                      >
                        {(regionGradientStops[slug] ?? []).map((s, i) => (
                          <stop key={i} offset={s.offset} stopColor={s.color} />
                        ))}
                      </linearGradient>
                    ))}
                  </defs>
                )}
                <CartesianGrid strokeDasharray="3 3" className="stroke-muted" />
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
                {/* No `domain`: recharts rescales from the data, so merging
                    pass 2 (raw, at the right-hand edge) CAN move the y-axis.
                    That is an accepted outcome of spec 2026-08-22-07 §2, not an
                    oversight — pinning the domain would flatten a genuine spike
                    in the newest points, which is the part of the chart people
                    are looking at, and the alternative (holding pass 1's scale
                    and letting raw overflow it) draws points outside the plot.
                    §2's "no visible jump" requirement is met by never
                    unmounting the chart (see chart-progressive-render.test.tsx);
                    the rescale itself is deliberate. */}
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
                          key &&
                          point &&
                          point[key as `durationMs:${string}`] != null
                        );
                      });
                      if (!entry) return null;
                      const data = entry.payload as ChartPoint;
                      if (data.durationMs == null) return null;
                      if (data.uid && data.uid === selectedUid) return null;
                      const key =
                        typeof entry.dataKey === "string"
                          ? entry.dataKey
                          : undefined;
                      const slug = key ? dataKeyToRegion.get(key) : undefined;
                      return (
                        <div className="rounded-md border bg-popover p-2 text-sm shadow-md">
                          {slug && (
                            <p className="flex items-center gap-1.5 font-medium">
                              <span
                                className="inline-block h-2 w-2 shrink-0 rounded-sm"
                                style={{
                                  backgroundColor: colorByRegion.get(slug),
                                }}
                                aria-hidden="true"
                              />
                              {regionLabel(slug)}
                            </p>
                          )}
                          <p className="text-muted-foreground">
                            {format(new Date(data.ts), "MMM d, HH:mm:ss")}
                          </p>
                          <p className="font-medium">
                            {formatMs(data.durationMs)}
                          </p>
                          {data.status && data.status !== "up" && (
                            <StatusBadge
                              status={data.status}
                              className="mt-1 text-xs"
                            />
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
                          <StatusBadge
                            status={data.status}
                            className="mt-1 text-xs"
                          />
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
                {/* In-progress drag-to-zoom selection band. */}
                {refAreaLeft != null && refAreaRight != null && (
                  <ReferenceArea
                    x1={Math.min(refAreaLeft, refAreaRight)}
                    x2={Math.max(refAreaLeft, refAreaRight)}
                    fill="var(--primary)"
                    fillOpacity={0.12}
                    stroke="var(--primary)"
                    strokeOpacity={0.4}
                  />
                )}
                {!isMultiSeries && (
                  <Area
                    type="monotone"
                    dataKey="durationMs"
                    stroke={`url(#strokeGradient-${checkUid})`}
                    fill={`url(#fillGradient-${checkUid})`}
                    strokeWidth={2}
                    connectNulls={false}
                    isAnimationActive={false}
                    dot={(props) => {
                      // Dense data → skip per-point circles, except for failing
                      // points: a down/unknown result always renders and stays
                      // clickable/pinnable, regardless of the density threshold.
                      // activeDot still renders the hover/selected dot and caches
                      // the anchor so the pinned-result box can find it.
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
                      // Selected point always renders (even in dense views) so its
                      // anchor is cached for the pinned box on a cold deep-link.
                      if (
                        !dotsEnabled &&
                        !isDownPoint(payload) &&
                        payload.uid !== selectedUid
                      ) {
                        return <g key={reactKey} />;
                      }
                      const uid = payload.uid;
                      // Cache the dot's coordinates for the pinned-box anchor.
                      // Mutating a ref outside the React commit phase is safe —
                      // it doesn't trigger a re-render.
                      dotPositions.current[uid] = { cx, cy };
                      const fill = dotFillColor(payload.status, COLOR_DOWN, COLOR_UP);
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
                    }}
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
                      const fill = dotFillColor(payload.status, COLOR_DOWN, COLOR_UP);
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
                        stroke={`url(#rt-region-${slug})`}
                        fill={`url(#rt-region-${slug})`}
                        fillOpacity={0.08}
                        strokeWidth={2}
                        connectNulls={false}
                        isAnimationActive={false}
                        dot={(props) => {
                          // Same combined-total dot-density threshold as
                          // single-series mode (totalPointCount / dotsEnabled),
                          // except a failing point always renders regardless.
                          const dotProps = props as {
                            cx?: number;
                            cy?: number;
                            payload?: ChartPoint;
                            key?: React.Key | null;
                          };
                          const {
                            cx,
                            cy,
                            payload,
                            key: reactKeyRaw,
                          } = dotProps;
                          const reactKey =
                            reactKeyRaw == null
                              ? undefined
                              : (reactKeyRaw as React.Key);
                          if (cx == null || cy == null || !payload?.uid) {
                            return <g key={reactKey} />;
                          }
                          if (
                            !dotsEnabled &&
                            !isDownPoint(payload) &&
                            payload.uid !== selectedUid
                          ) {
                            return <g key={reactKey} />;
                          }
                          const uid = payload.uid;
                          dotPositions.current[uid] = { cx, cy };
                          // Down results stay visible as red dots on
                          // their line even though the line itself uses
                          // the region color, not the status gradient.
                          const fill = dotFillColor(payload.status, COLOR_DOWN, color);
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
                        }}
                        activeDot={(props) => {
                          const dotProps = props as {
                            cx?: number;
                            cy?: number;
                            payload?: ChartPoint;
                            key?: React.Key | null;
                          };
                          const {
                            cx,
                            cy,
                            payload,
                            key: reactKeyRaw,
                          } = dotProps;
                          const reactKey =
                            reactKeyRaw == null
                              ? undefined
                              : (reactKeyRaw as React.Key);
                          if (cx == null || cy == null || !payload?.uid) {
                            return <g key={reactKey} />;
                          }
                          const uid = payload.uid;
                          dotPositions.current[uid] = { cx, cy };
                          const fill = dotFillColor(payload.status, COLOR_DOWN, color);
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
                onClose={() => onSelectChange?.(undefined)}
              />
            )}
          </div>
        )}
        {/* Availability strip, sharing the chart's x-scale, window, zoom and
            region filter. Inset by the chart's own left gutter (YAxis width +
            the AreaChart default margin) so a cell sits under the slice of the
            plot it describes rather than under the y-axis labels.

            Not rendered below the 3h floor: the header figure above is the
            fallback there (see @/lib/availability-strip). */}
        {!isLoading && chartData.length > 0 && stripVisible && (
          <div
            className="mt-1"
            style={{
              paddingLeft: CHART_PLOT_INSET_LEFT,
              paddingRight: CHART_PLOT_INSET_RIGHT,
            }}
          >
            {availabilityLoading && !availability ? (
              <Skeleton
                className="h-3 w-full"
                data-testid="availability-strip-loading"
              />
            ) : availability?.data.length ? (
              <AvailabilityStrip
                cells={availability.data}
                testIdPrefix="response-time-chart-availability-strip"
                height="sm"
              />
            ) : null}
          </div>
        )}
        {!isLoading && chartData.length > 0 && (
          <p className="mt-2 text-center text-xs text-muted-foreground">
            {zoomed
              ? t("detail.chart.zoomHintReset")
              : t("detail.chart.zoomHint")}
          </p>
        )}
      </CardContent>
    </Card>
  );
}
