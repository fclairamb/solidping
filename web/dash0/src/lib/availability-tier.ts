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

/**
 * The tier badge on the dashboard's hero availability tile (spec
 * 2026-09-24-02): a SOLID white chip, identical in both themes, carrying the
 * tier's light-theme text color. A pale translucent status badge would lose
 * its meaning on the gradient. Each text color reads >= 4.5:1 on white at the
 * badge's 11px (kpi-tile.test.tsx checks it): emerald-700, amber-700, and
 * red-700 for "down" — a plain Tailwind color matching the other three
 * tiers' own fixed palette, kept separate from --destructive (a different
 * token, tuned for the destructive button/text elsewhere in the app; spec
 * 2026-09-24-07 darkened it enough to also clear 4.5:1 on white, but the two
 * were never coupled).
 */
export const AVAILABILITY_TIER_HERO_BADGE: Record<AvailabilityTier, string> = {
  noData: "bg-white text-slate-600",
  operational: "bg-white text-emerald-700",
  degraded: "bg-white text-amber-700",
  down: "bg-white text-red-700",
};
