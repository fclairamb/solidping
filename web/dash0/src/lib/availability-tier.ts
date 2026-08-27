// Badge tier for the org dashboard's "24h Availability" KPI (spec
// 2026-08-26-09). Split out of dashboard-page.tsx so the pure logic is
// directly unit-testable and the component file keeps a component-only
// export surface (react-refresh/only-export-components).

// Thresholds for the badge tier — named constants rather than magic numbers
// scattered through JSX, and a starting point per the spec, not a settled
// SLA policy.
export const AVAILABILITY_OPERATIONAL_PCT = 99.9;
export const AVAILABILITY_DEGRADED_PCT = 99;

export type AvailabilityTier = "noData" | "operational" | "degraded" | "down";

/**
 * Derives the badge tier from a nullable availability percentage.
 * `null` (no countable data — an empty or brand-new org, or a stats query
 * that hasn't resolved yet) maps to "noData", never to a fabricated tier.
 */
export function availabilityTier(pct: number | null): AvailabilityTier {
  if (pct === null) return "noData";
  if (pct >= AVAILABILITY_OPERATIONAL_PCT) return "operational";
  if (pct >= AVAILABILITY_DEGRADED_PCT) return "degraded";
  return "down";
}
