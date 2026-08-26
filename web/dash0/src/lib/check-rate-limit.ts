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
 * Two independent triggers, and both are needed:
 *
 * - **Predictive** — demand is over the cap, so executions are about to be (or
 *   already are) skipped, even if today's counter has not caught up yet.
 * - **Factual** — the rate gate skipped executions today. An org that just
 *   deleted a few checks and dropped back under its cap still has gaps in
 *   today's history, and the banner is the only thing that explains them.
 *
 * The counter resets at UTC midnight, so the factual half clears itself once
 * the org spends a full day under its cap.
 */
export function shouldWarnAboutCheckRate(
  cpm?: EntitlementsChecksPerMinute | null,
): boolean {
  if (!cpm) {
    return false;
  }

  return cpm.skippedToday > 0 || isOverCheckRateLimit(cpm);
}

/**
 * Formats a demand figure for display: whole numbers stay whole, fractional
 * rates (a check every 5 minutes contributes 0.2) keep one decimal.
 */
export function formatCheckRateDemand(demand: number): string {
  return Number.isInteger(demand) ? String(demand) : demand.toFixed(1);
}
