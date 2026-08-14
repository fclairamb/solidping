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
  [field: string]: string | number | null | undefined;
}

// Pivots per-region series into ONE array of rows keyed by the sorted union
// of timestamps across every series (recharts needs one shared data array to
// render several Areas against the same x-axis). Each row also carries a
// rolled-up `status` — the worst status among the regions with a REAL point
// at that exact timestamp — which drives the single incident strip.
export function buildCombinedRows(series: ResponseTimeSeries[]): CombinedRow[] {
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
