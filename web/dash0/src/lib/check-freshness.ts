// Freshness helpers for the "No data" (stale) status, spec 2026-09-25-02.
//
// A check is stale when its newest REAL result, across all regions, is older
// than max(3 × period, 5 min). The server owns that decision (checks.status);
// these helpers only read what it sends — lastResultAt, staleThresholdSeconds
// and the per-region regionFreshness list — so the dashboard can never
// disagree with the API about what "no data" means.

import type { Check, RegionFreshness } from "@/api/hooks";

/** Result statuses that are not a reading of the service. */
const NOT_A_READING = new Set(["abandoned", "created", "running"]);

/**
 * The newest REAL result time: the server's lastResultAt (which never counts
 * abandoned or lifecycle rows), falling back to the embedded lastResult only
 * when that row is itself a real reading. "Last checked" used to read the
 * newest row of any kind, so a reaped attempt from a live region hid a dead
 * one.
 */
export function lastRealResultAt(check: Pick<Check, "lastResultAt" | "lastResult">): string | undefined {
  if (check.lastResultAt) return check.lastResultAt;
  const last = check.lastResult;
  if (last?.timestamp && last.status && !NOT_A_READING.has(last.status)) {
    return last.timestamp;
  }
  return undefined;
}

/** Splits a check's per-region freshness into the silent regions and the
 * number still reporting. */
export function splitFreshness(freshness: RegionFreshness[] | undefined): {
  silent: RegionFreshness[];
  reporting: RegionFreshness[];
} {
  const silent: RegionFreshness[] = [];
  const reporting: RegionFreshness[] = [];
  for (const region of freshness ?? []) {
    (region.stale ? silent : reporting).push(region);
  }
  return { silent, reporting };
}

/** True when some regions report and others are silent — the case where a
 * single "last checked" hides a dead region behind a live one. */
export function regionsDisagree(freshness: RegionFreshness[] | undefined): boolean {
  const { silent, reporting } = splitFreshness(freshness);
  return silent.length > 0 && reporting.length > 0;
}

/**
 * "13:41" for today, "24 Sep 13:41" otherwise — the wall-clock form an
 * operator compares against their own clock ("no data since 13:41").
 */
export function formatClockTime(iso: string, locale?: string, now: Date = new Date()): string {
  const date = new Date(iso);
  const time = date.toLocaleTimeString(locale, { hour: "2-digit", minute: "2-digit" });
  const sameDay =
    date.getFullYear() === now.getFullYear() &&
    date.getMonth() === now.getMonth() &&
    date.getDate() === now.getDate();
  if (sameDay) return time;
  const day = date.toLocaleDateString(locale, { day: "numeric", month: "short" });
  return `${day} ${time}`;
}
