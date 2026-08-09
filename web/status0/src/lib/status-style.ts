// Single source of truth for how a check / availability status is rendered on
// the public status page (status0). Every status surface — the status hero,
// resource dots and badges, the availability bars, and the response-time
// chart — routes through statusStyle() so the warning/degraded amber
// treatment stays consistent.
//
// Two distinct amber states:
//   - "warning"  — the live current status ("up, but something to report").
//   - "degraded" — the aggregated rollup status (a window had warning(s) but
//                  no dominating failure).
// Both render amber; they differ only in their label.

export type BadgeVariant =
  | "success"
  | "warning"
  | "destructive"
  | "secondary"
  | "info";

export interface StatusStyle {
  // Tailwind background class for solid swatches (dots, availability bars).
  color: string;
  // recharts / inline SVG fill colour (hex), neutral when not down.
  chartColor: string;
  // Badge variant from components/ui/badge.tsx.
  badgeVariant: BadgeVariant;
  // i18n label key (top-level status.json key, e.g. "degraded").
  labelKey: string;
  // True when this status reads as a hard failure (down / error).
  isDown: boolean;
}

const NEUTRAL_CHART = "transparent";
const DOWN_CHART = "#ef4444"; // red-500
const WARNING_CHART = "#facc15"; // yellow-400

export function statusStyle(status: string | undefined | null): StatusStyle {
  switch (status) {
    case "ok":
    case "up":
    case "operational":
      return {
        color: "bg-green-500",
        chartColor: NEUTRAL_CHART,
        badgeVariant: "success",
        labelKey: "operational",
        isDown: false,
      };
    case "warning":
      return {
        color: "bg-yellow-500",
        chartColor: WARNING_CHART,
        badgeVariant: "warning",
        labelKey: "warning",
        isDown: false,
      };
    case "degraded":
      return {
        color: "bg-yellow-500",
        chartColor: WARNING_CHART,
        badgeVariant: "warning",
        labelKey: "degraded",
        isDown: false,
      };
    case "error":
    case "down":
      return {
        color: "bg-red-500",
        chartColor: DOWN_CHART,
        badgeVariant: "destructive",
        labelKey: "outage",
        isDown: true,
      };
    // Page-level rollup only (spec 2026-08-08-05) — no individual check ever
    // reports this status itself, but the page-level overallStatus does when
    // a resource is inside an active maintenance window. Blue/info, distinct
    // from the amber warning/degraded treatment: maintenance is planned, not
    // a fault.
    case "maintenance":
      return {
        color: "bg-blue-500",
        chartColor: NEUTRAL_CHART,
        badgeVariant: "info",
        labelKey: "underMaintenance",
        isDown: false,
      };
    default:
      // Also covers the page-level "unknown" rollup (spec 2026-08-08-05,
      // no resource has a usable status yet) — same muted/gray treatment as
      // an individual resource with no live data. The hero renders its own
      // "Status Unknown" label text; this default's labelKey stays
      // "unknown" for the per-resource case.
      return {
        color: "bg-gray-400",
        chartColor: NEUTRAL_CHART,
        badgeVariant: "secondary",
        labelKey: "unknown",
        isDown: false,
      };
  }
}
