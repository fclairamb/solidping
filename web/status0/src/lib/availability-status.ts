/**
 * The availability classification, as the public status page sees it.
 *
 * The authority is the SERVER — `uptimebar.Classify`
 * (server/internal/uptimebar/classify.go), which the availability bar, the badge
 * uptime bar and the operator dashboard all share; every response-time point
 * already arrives with its `availabilityStatus` resolved against the page's own
 * thresholds.
 *
 * This module exists for the ONE case the server cannot answer: the multi-region
 * response-time chart merges several regions into one x-axis slot on the client
 * (slot grouping is a rendering decision — see lib/response-time-rollup.ts), so
 * the merged cell has to be classified here. It reproduces the Go rule exactly,
 * including the small-bucket guard, rather than inventing a second mapping.
 */

import type { AvailabilityStatus } from "@/api/hooks";
import { statusStyle } from "./status-style";

/** models.DefaultAvailabilityThreshold{Up,Degraded}. */
export const AVAILABILITY_THRESHOLD_UP = 99.9;
export const AVAILABILITY_THRESHOLD_DEGRADED = 99.0;

/**
 * The TypeScript twin of `uptimebar.ClassifyStats`: `up`/`total` are summed
 * across whatever contributed to the cell — SUMMED, never averaged as
 * percentages, so a region that reported three probes weighs three times one
 * that reported one.
 *
 * `total === 0` is "no data", explicitly not 100%.
 */
export function classifyAvailabilityCounts(
  up: number,
  total: number,
  upThreshold = AVAILABILITY_THRESHOLD_UP,
  degradedThreshold = AVAILABILITY_THRESHOLD_DEGRADED,
): AvailabilityStatus {
  if (total <= 0) return "noData";
  const pct = (up / total) * 100;
  if (pct >= upThreshold) return "up";
  if (pct >= degradedThreshold) return "degraded";
  // Small-bucket calibration guard: red needs at least two failed samples, so a
  // single failed probe never turns a whole slot red.
  if (total - up <= 1) return "degraded";
  return "down";
}

/** SVG fill for one availability cell. noData keeps the muted half-strength
 * treatment the availability bar's own no-data segments use, so the two strips
 * on the same card read as one system. */
export function availabilityFill(status: AvailabilityStatus | undefined): {
  fill: string;
  opacity: number;
} {
  if (!status || status === "noData")
    return { fill: "var(--status-neutral)", opacity: 0.4 };
  return { fill: statusStyle(status).barFill, opacity: 1 };
}

/** Formats an availability percentage the way every SolidPing surface does. */
export function formatAvailabilityPct(pct: number | undefined): string | null {
  if (pct === undefined || Number.isNaN(pct)) return null;
  if (pct >= 100) return "100%";
  if (pct >= 99) return `${pct.toFixed(2)}%`;
  return `${pct.toFixed(1)}%`;
}
