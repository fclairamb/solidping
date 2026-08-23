import { subDays, subHours, startOfMinute } from "date-fns";

/** The chart's default time ranges. */
export type TimeRange = "hour" | "day" | "week" | "month";

/** Fields fetched for the chart window — extended with stored aggregate stats
 * (durationMinMs/MaxMs/AvgMs/P95Ms, totalChecks) so the Recent Results stats
 * strip can compute tier-aware min/avg/max/p95 from this same dataset without
 * an extra HTTP request. Raw rows simply omit the aggregate-only fields. */
export const CHART_WITH_FIELDS =
  "durationMs,region,durationMinMs,durationMaxMs,durationAvgMs,durationP95Ms,totalChecks";

/** One tier's fetch parameters. `periodType` is either a rollup list
 * ("hour", "hour,day") or exactly "raw" — **never both**. The two partial
 * indexes on `results` are split on `period_type = 'raw'` vs
 * `period_type != 'raw'`, so a list straddling that split is provably
 * satisfied by neither and can only be answered by a sequential scan of the
 * largest table in the system (spec 2026-08-22-04). */
export interface ChartTierFetch {
  periodStartAfter: string;
  /** Only present when a zoom window is active — maps the URL `graphTo` onto the
   * results endpoint's existing `periodEndBefore` (RFC3339) filter. */
  periodEndBefore?: string;
  periodType: string;
  with: string;
  size: number;
}

/** A drag-selected X (time) zoom window, epoch-ms, `from < to`. */
export interface ZoomWindow {
  from: number;
  to: number;
}

/** The chart's default window start for a range, as RFC3339. */
export function getStartFor(range: TimeRange): string {
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

/** Maps a zoom span (ms) onto the equivalent default TimeRange bucket, so the
 * aggregation-tier choice for a zoomed window matches what that span would use
 * as a normal range (a narrow window naturally lands on the finer, less
 * aggregated tiers). */
export function rangeForSpan(spanMs: number): TimeRange {
  const ONE_HOUR = 3_600_000;
  const ONE_DAY = 24 * ONE_HOUR;
  if (spanMs <= ONE_HOUR) return "hour";
  if (spanMs <= ONE_DAY) return "day";
  if (spanMs <= 7 * ONE_DAY) return "week";
  return "month";
}

/** The rollup tier list for a window, or "" when raw IS the tier for it (an
 * hour view, or a day view of a check slow enough that raw is already sparse).
 * Exported so a caller can tell "pass 1 has nothing to fetch" from "pass 1
 * returned nothing", which are different situations for the seam. */
export function chartRollupTier(
  timeRange: TimeRange,
  periodMs: number | undefined,
  zoom?: ZoomWindow,
): string {
  const effectiveRange = zoom ? rangeForSpan(zoom.to - zoom.from) : timeRange;
  const denseEnoughForHourly = (periodMs ?? 60_000) < 5 * 60_000;

  return effectiveRange === "month"
    ? "hour,day"
    : effectiveRange === "week"
      ? "hour"
      : effectiveRange === "day" && denseEnoughForHourly
        ? "hour"
        : "";
}

/** The window bounds every tier of a plan shares. */
export function chartWindowBounds(
  timeRange: TimeRange,
  zoom?: ZoomWindow,
): Pick<ChartTierFetch, "periodStartAfter" | "periodEndBefore"> {
  return zoom
    ? {
        periodStartAfter: new Date(zoom.from).toISOString(),
        periodEndBefore: new Date(zoom.to).toISOString(),
      }
    : { periodStartAfter: getStartFor(timeRange) };
}

/** Row shape `seamStartFrom` needs — structural, so this module stays free of
 * an import cycle with `@/api/hooks`. */
export interface SeamAnchorRow {
  periodStart?: string;
  region?: string;
}

/**
 * Where the raw (pass 2) window starts, given the rollup rows pass 1 returned.
 *
 * Raw retention is 24 h by default, so "raw" is not the current open bucket —
 * it is a full day of samples at the check's period, per region. On a month view
 * that is ~4 320 points fighting for 3 % of the x-axis, five sequential HTTP
 * round-trips, re-fetched every 60 s. Everything older than the newest rollup
 * bucket is already represented at a density the chart can actually draw, so
 * raw is only needed for the SEAM: newest rollup bucket → now
 * (spec 2026-08-22-07 §1).
 *
 * The boundary comes from pass 1's own data rather than a fixed "last hour"
 * constant, and that is the whole point: when the aggregator is lagging the
 * newest rollup is older, the seam widens on its own, and raw fills exactly the
 * span no rollup covers. A constant would silently truncate the chart's
 * right-hand edge in precisely that case.
 *
 * Two deliberate choices:
 *
 * - **Anchored on `periodStart`, not `periodEnd`.** The aggregator compacts a
 *   bucket and deletes its source raw rows in ONE transaction, so the newest
 *   bucket's own span contains no raw at all. Starting at its `periodStart`
 *   therefore costs an empty sliver of index range and can never leave a gap,
 *   while `periodEnd` (optional on the wire) would need a fallback anyway.
 * - **Per-region minimum of the per-region maxima.** Rollup rows arrive per
 *   region and regions can lag independently; a global maximum would set the
 *   seam from the healthiest region and leave a lagging one with neither rollup
 *   nor raw over the difference.
 *
 * Returns `undefined` when pass 1 returned no usable rows — a check younger than
 * one rollup bucket has raw and nothing else, so raw must then span the full
 * window rather than be narrowed to nothing.
 */
export function seamStartFrom(
  rollupRows: readonly SeamAnchorRow[] | undefined,
): string | undefined {
  if (!rollupRows?.length) return undefined;

  const newestByRegion = new Map<string, number>();

  for (const row of rollupRows) {
    if (!row.periodStart) continue;

    const ts = Date.parse(row.periodStart);
    if (Number.isNaN(ts)) continue;

    const key = row.region ?? "";
    const current = newestByRegion.get(key);
    if (current === undefined || ts > current) newestByRegion.set(key, ts);
  }

  if (newestByRegion.size === 0) return undefined;

  const seam = Math.min(...newestByRegion.values());

  return new Date(seam).toISOString();
}

/** Builds the chart's **tier plan** for the current window (time range + check
 * period, or an explicit zoom window): at most two entries, the rollup tier
 * first and the raw tier second, never one query mixing the two. Exported so
 * the results-list route can issue the identical set of queries (same
 * react-query keys) to derive the observed region set and duration stats from
 * the chart's already-fetched window, with zero extra HTTP requests. When
 * `zoom` is passed, the window is `[zoom.from, zoom.to]` (server-side fetch,
 * not a client re-scale) and the tier is chosen from the zoom span.
 *
 * Raw is always requested alongside the rollups so the current open bucket
 * (which the aggregator never rolls up until it closes) is represented — but
 * as its OWN query, because `period_type IN ('raw','hour')` is implied by
 * neither partial index on `results` and forces a full sequential scan. The
 * aggregator deletes source rows after rollup, so raw + hour + day stay
 * disjoint in time; merging the tiers client-side yields exactly the rows the
 * single mixed query returned.
 *
 * `rawStartAfter` narrows the raw entry to the seam (see `seamStartFrom`). It
 * is ignored when there is no rollup tier — raw IS the tier for that window, so
 * narrowing it would delete the chart's data rather than its redundancy — and
 * it is clamped to the window start so a caller can never widen the fetch past
 * what was asked for.
 *
 * **If you change the tier lists this can emit, update the Go plan tests too.**
 * They enumerate these exact lists and EXPLAIN each one against a production-
 * sized fixture; nothing but this comment links the two languages, so a new
 * tier list added here is a query nobody has ever seen a plan for:
 *   - server/internal/db/postgres/chart_results_plan_postgres_test.go
 *   - server/internal/db/sqlite/chart_results_plan_test.go
 * The local guard that the lists never re-mix raw with a rollup tier lives in
 * web/dash0/src/components/checks/chart-fetch-params.test.ts. */
export function chartFetchParams(
  timeRange: TimeRange,
  periodMs: number | undefined,
  zoom?: ZoomWindow,
  rawStartAfter?: string,
): ChartTierFetch[] {
  const rollupTier = chartRollupTier(timeRange, periodMs, zoom);
  const window = chartWindowBounds(timeRange, zoom);

  const tiers: ChartTierFetch[] = [];

  if (rollupTier) {
    tiers.push({
      ...window,
      periodType: rollupTier,
      with: CHART_WITH_FIELDS,
      size: 1000,
    });
  }

  const rawStart =
    rollupTier && rawStartAfter && rawStartAfter > window.periodStartAfter
      ? rawStartAfter
      : window.periodStartAfter;

  tiers.push({
    ...window,
    periodStartAfter: rawStart,
    periodType: "raw",
    with: CHART_WITH_FIELDS,
    size: 1000,
  });

  return tiers;
}
