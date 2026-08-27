import type { ResponseTimeSeries } from "@/api/hooks";

// Severity rank used to roll several regions' statuses up into ONE incident
// strip color for a shared timestamp — "worst status wins" (spec
// 2026-08-14-04, Proposal item 5). Higher wins; anything not listed (up,
// created, running, "unknown", undefined) ranks 0, i.e. never overrides a
// real incident.
export const STATUS_SEVERITY: Record<string, number> = {
  down: 4,
  error: 3,
  timeout: 2,
  degraded: 1,
  warning: 1,
};

export function severityRank(status?: string): number {
  if (!status) return 0;
  return STATUS_SEVERITY[status] ?? 0;
}

// Stable per-series data-key naming, shared between the pivot below and the
// chart component that renders one Area/Tooltip entry per index.
export function pointKey(index: number): string {
  return `p${index}`;
}

export function statusFieldKey(index: number): string {
  return `st${index}`;
}

export interface CombinedRow {
  time: string;
  status?: string;
  /** Probes counted as up across every region contributing to this slot, and
   * the countable total. SUMMED, never averaged as percentages — a region that
   * reported sixty probes must weigh sixty times one that reported one (the
   * server applies the same rule when it buckets "all regions"). */
  availUp?: number;
  availTotal?: number;
  [field: string]: string | number | null | undefined;
}

// Regions do NOT sample in lockstep: each worker has its own few-seconds phase
// inside the check interval, so five regions checking every minute produce five
// DISTINCT timestamps per minute (…:26:08, :26:26, :26:31, :26:43, :25:58).
// Keyed on the raw timestamp that pivots into 5x as many rows, each holding one
// region's value and four nulls — and since every Area is drawn with
// connectNulls={false}, a series whose neighbours are always null draws no line
// segment at all and the chart renders BLANK. So points are grouped into shared
// slots first, by sweeping them in time order.
//
// Tolerance for that sweep, as a fraction of the sampling interval: wide enough
// to absorb the phase spread between regions, below 1.0 so two consecutive
// samples of the same series can never collapse into one slot (the
// one-point-per-series rule below is the hard guarantee; this keeps it from
// having to fire in the common case).
const SLOT_TOLERANCE_RATIO = 0.9;

function medianConsecutiveDelta(sortedTimes: number[]): number | null {
  const deltas: number[] = [];
  for (let i = 1; i < sortedTimes.length; i++) {
    const delta = sortedTimes[i] - sortedTimes[i - 1];
    if (delta > 0) deltas.push(delta);
  }
  if (deltas.length === 0) return null;
  deltas.sort((a, b) => a - b);
  return deltas[Math.floor(deltas.length / 2)];
}

// The finest sampling interval across the series — median, not mean, so a
// single gap (a worker restart, a paused check) does not stretch it. Null when
// no series has two usable points, in which case the caller falls back to
// exact-timestamp grouping, i.e. the original behaviour.
export function samplingInterval(series: ResponseTimeSeries[]): number | null {
  let finest: number | null = null;
  for (const s of series) {
    const times = s.points
      .map((p) => Date.parse(p.time))
      .filter((ms) => Number.isFinite(ms))
      .sort((a, b) => a - b);
    const delta = medianConsecutiveDelta(times);
    if (delta != null && (finest == null || delta < finest)) finest = delta;
  }
  return finest;
}

// Pivots per-region series into ONE array of rows, one per time slot across
// every series (recharts needs one shared data array to render several Areas
// against the same x-axis). Each row also carries a rolled-up `status` — the
// worst status among the regions with a REAL point in that slot — which drives
// the single incident strip.
export function buildCombinedRows(series: ResponseTimeSeries[]): CombinedRow[] {
  const interval = samplingInterval(series);
  // 0 with no derivable interval: only points at the very same instant group,
  // which is what this function did before slots existed.
  const tolerance = interval == null ? 0 : interval * SLOT_TOLERANCE_RATIO;

  const samples = series
    .flatMap((s, index) =>
      s.points.map((point) => ({
        index,
        point,
        ms: Date.parse(point.time),
      })),
    )
    // Ties broken by series order so the pivot is deterministic across renders.
    .sort((a, b) => a.ms - b.ms || a.index - b.index);

  const rows: CombinedRow[] = [];
  let current: CombinedRow | null = null;
  let slotStart = 0;
  let filled = new Set<number>();

  for (const sample of samples) {
    const startsNewSlot =
      current == null ||
      !Number.isFinite(sample.ms) ||
      sample.ms - slotStart > tolerance ||
      // One point per series per slot — never let two samples of the same
      // region overwrite each other just because they fell inside the window.
      filled.has(sample.index);

    if (startsNewSlot) {
      // The slot is labelled with its first point's own timestamp rather than a
      // synthetic slot centre, so the axis and tooltip keep showing a time the
      // data actually reported at.
      current = { time: sample.point.time };
      rows.push(current);
      // -Infinity for an unparseable timestamp, so the next sample is forced
      // into a slot of its own instead of silently joining this one.
      slotStart = Number.isFinite(sample.ms) ? sample.ms : -Infinity;
      filled = new Set();
    }

    const row = current as CombinedRow;
    row[pointKey(sample.index)] = sample.point.durationP95 ?? null;
    row[statusFieldKey(sample.index)] = sample.point.status;
    // Availability sums across the regions that actually reported in this slot.
    // A point with no countable probe (a lifecycle marker, an abandoned
    // attempt) carries no counts and therefore contributes nothing — which is
    // not the same as contributing zero.
    if (sample.point.totalChecks) {
      row.availUp = (row.availUp ?? 0) + (sample.point.successfulChecks ?? 0);
      row.availTotal = (row.availTotal ?? 0) + sample.point.totalChecks;
    }
    filled.add(sample.index);
  }

  for (const row of rows) {
    let worstStatus: string | undefined;
    let worstRank = -1;
    series.forEach((_, index) => {
      // Only regions with an actual point at this timestamp are candidates —
      // a region with no point here must never contribute a phantom "up"/
      // undefined vote that could out-rank a real status from another region.
      const key = statusFieldKey(index);
      if (!(key in row)) return;
      const status = row[key] as string | undefined;
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
