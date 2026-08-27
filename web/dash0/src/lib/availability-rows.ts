import type { CheckAvailabilityPeriod } from "@/api/hooks";
import { formatAvailabilityNumber } from "@/lib/availability-status";

/**
 * Pure mapping from a server-measured {@link CheckAvailabilityPeriod} (or its
 * absence) to the display cells of one availability-table row. Extracted from
 * the component so it can be unit-tested without a DOM — the component renders
 * straight from this, doing no availability math of its own.
 *
 * - `availabilityText` is "-" when the window has no data (no data ≠ 100%).
 * - `downtimeText` is "-" when there is no data; otherwise probe-time downtime.
 * - `monitoredDays` is non-null only on partial rows (check younger than the
 *   window), so the caller can caveat it (e.g. "7d monitored").
 */
export interface AvailabilityRowView {
  availabilityText: string;
  downtimeText: string;
  incidentCount: number;
  longestText: string | null;
  averageText: string | null;
  monitoredDays: number | null;
}

const SECONDS_PER_DAY = 86_400;

/** Re-exported from the shared availability module so the table and the chart
 * strip sitting above it format the same number identically — they are the two
 * surfaces spec 2026-08-26-10 exists to line up. */
export const formatAvailabilityPct = formatAvailabilityNumber;

export function formatDurationSeconds(totalSeconds: number): string {
  if (totalSeconds <= 0) return "0s";
  const seconds = Math.floor(totalSeconds);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);

  if (days > 0) return `${days}d ${hours % 24}h`;
  if (hours > 0) return `${hours}h ${minutes % 60}m`;
  if (minutes > 0) return `${minutes}m ${seconds % 60}s`;
  return `${seconds}s`;
}

/** One rendered availability row: which requested period it maps to, and
 * whether it is the collapsed catch-all standing for the whole monitored
 * history (see {@link selectAvailabilityRows}). */
export interface AvailabilityRowSelection {
  index: number;
  collapsed: boolean;
}

/**
 * Decide which requested periods to actually render, given the server periods
 * in display order (shortest window first).
 *
 * Every period whose window starts before the check existed (`partial`) is
 * measured over exactly the same span — the check's whole life `[createdAt,
 * now]` — so they carry byte-identical availability, downtime and incident
 * numbers. Rendering all of them is the "copy-paste rows" problem a young check
 * exhibits (Today = Last 7 days = Last 30 days = Last 365 days).
 *
 * So we keep every non-partial period (genuinely distinct windows) in order,
 * then collapse the partial tail into a single catch-all row (`collapsed: true`)
 * that stands for the whole monitored history, and drop the rest. `partial` is
 * monotonic once the windows are ordered shortest-first (window starts only move
 * further into the past, and `createdAt` is fixed), so the kept partial period
 * is always the first one and everything after it is redundant. A period missing
 * from the response (`undefined`) counts as non-partial no-data and is kept in
 * place so it still renders its dashes.
 */
export function selectAvailabilityRows(
  periods: readonly (CheckAvailabilityPeriod | undefined)[],
): AvailabilityRowSelection[] {
  const rows: AvailabilityRowSelection[] = [];

  for (let index = 0; index < periods.length; index++) {
    if (periods[index]?.partial) {
      rows.push({ index, collapsed: true });
      break;
    }

    rows.push({ index, collapsed: false });
  }

  return rows;
}

export function mapAvailabilityRow(
  period: CheckAvailabilityPeriod | undefined,
): AvailabilityRowView {
  const incidentCount = period?.incidents.count ?? 0;
  const hasData = period?.hasData ?? false;

  return {
    availabilityText:
      hasData && period?.availabilityPct != null
        ? formatAvailabilityPct(period.availabilityPct)
        : "-",
    downtimeText: hasData ? formatDurationSeconds(period!.downtimeSeconds) : "-",
    incidentCount,
    longestText:
      incidentCount > 0
        ? formatDurationSeconds(period?.incidents.longestSeconds ?? 0)
        : null,
    averageText:
      incidentCount > 0
        ? formatDurationSeconds(period?.incidents.averageSeconds ?? 0)
        : null,
    monitoredDays: period?.partial
      ? Math.max(1, Math.round(period.monitoredSeconds / SECONDS_PER_DAY))
      : null,
  };
}
