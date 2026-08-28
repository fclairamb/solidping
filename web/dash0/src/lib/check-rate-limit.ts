import type { EntitlementsChecksPerMinute } from "@/api/hooks";

/**
 * Whether the org's scheduled demand exceeds its per-minute execution cap.
 *
 * An absent/null limit is "unlimited" and can never be exceeded — the same
 * convention every other entitlement cap uses.
 */
export function isOverCheckRateLimit(cpm?: EntitlementsChecksPerMinute | null): boolean {
  if (!cpm) {
    return false;
  }

  return (
    cpm.limit !== null && cpm.limit !== undefined && cpm.demand > cpm.limit
  );
}

/**
 * Whether to warn the org that its check executions are being dropped.
 *
 * Keyed on the live state only: demand over the cap means executions are
 * being (or are about to be) skipped, and the warning is actionable. The
 * moment the org brings its scheduling back under the cap — the very thing
 * the banner asks for — the warning disappears, instead of lingering until
 * the UTC-midnight counter reset just because `skippedToday` is non-zero.
 * Today's skip count still appears inside the banner as supporting detail
 * while it shows.
 */
export function shouldWarnAboutCheckRate(
  cpm?: EntitlementsChecksPerMinute | null,
): boolean {
  return isOverCheckRateLimit(cpm);
}

/**
 * Formats a demand figure for display: whole numbers stay whole, fractional
 * rates (a check every 5 minutes contributes 0.2) keep one decimal.
 */
export function formatCheckRateDemand(demand: number): string {
  return Number.isInteger(demand) ? String(demand) : demand.toFixed(1);
}
