import type { CheckAvailabilityPeriod } from "@/api/hooks";

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

export function formatAvailabilityPct(pct: number): string {
  if (pct >= 100) return "100%";
  if (pct >= 99) return `${pct.toFixed(2)}%`;
  return `${pct.toFixed(1)}%`;
}

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
