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
  // Tailwind background class for solid swatches (dots, badges).
  color: string;
  // The same solid colour as a CSS colour value, for SVG `fill`. The
  // availability bar is drawn rather than laid out (see lib/segment-layout.ts),
  // and an SVG shape cannot wear a `bg-*` class.
  barFill: string;
  // recharts / inline SVG fill colour (hex), neutral when not down.
  chartColor: string;
  // Badge variant from components/ui/badge.tsx.
  badgeVariant: BadgeVariant;
  // i18n label key (top-level status.json key, e.g. "degraded").
  labelKey: string;
  // True when this status reads as a hard failure (down / error).
  isDown: boolean;
  // Tailwind classes for the full-width hero banner: border + tinted gradient
  // fill. Ported from dash0's OverallStatusBanner
  // (web/dash0/src/components/dashboard/dashboard-page.tsx) so the operator
  // console and the public page announce a state the same way. Only the
  // page-level rollup uses these; per-resource rows never render a banner.
  bannerSurface: string;
  // Tailwind text colour for the banner headline.
  bannerTitle: string;
  // Tailwind classes for the trailing pill on the banner.
  bannerPill: string;
}

const NEUTRAL_CHART = "transparent";
// Themed via CSS custom properties rather than hex literals, so the
// availability/response-time SVG fills track light and dark like every other
// surface. `var()` is legal in an SVG `fill`, and the tokens are defined on
// :root in index.css, so these resolve wherever the chart is mounted.
const DOWN_CHART = "var(--status-error)";
const WARNING_CHART = "var(--status-warning)";

export function statusStyle(status: string | undefined | null): StatusStyle {
  switch (status) {
    case "ok":
    case "up":
    case "operational":
      return {
        color: "bg-status-ok",
        barFill: "var(--status-ok)",
        chartColor: NEUTRAL_CHART,
        badgeVariant: "success",
        labelKey: "operational",
        bannerSurface: "border-status-ok/25 bg-gradient-to-r from-status-ok/[0.10] via-status-ok/[0.04] to-transparent",
        bannerTitle: "text-status-ok-foreground",
        bannerPill: "border-status-ok/25 bg-status-ok/10 text-status-ok-foreground",
        isDown: false,
      };
    case "warning":
      return {
        color: "bg-status-warning",
        barFill: "var(--status-warning)",
        chartColor: WARNING_CHART,
        badgeVariant: "warning",
        labelKey: "warning",
        bannerSurface: "border-status-warning/30 bg-gradient-to-r from-status-warning/[0.12] via-status-warning/[0.05] to-transparent",
        bannerTitle: "text-status-warning-foreground",
        bannerPill: "border-status-warning/30 bg-status-warning/15 text-status-warning-foreground",
        isDown: false,
      };
    case "degraded":
      return {
        color: "bg-status-warning",
        barFill: "var(--status-warning)",
        chartColor: WARNING_CHART,
        badgeVariant: "warning",
        labelKey: "degraded",
        bannerSurface: "border-status-warning/30 bg-gradient-to-r from-status-warning/[0.12] via-status-warning/[0.05] to-transparent",
        bannerTitle: "text-status-warning-foreground",
        bannerPill: "border-status-warning/30 bg-status-warning/15 text-status-warning-foreground",
        isDown: false,
      };
    case "abandoned":
      // Reaper-minted: nothing was ever reported for the attempt (our side
      // died, not the target). Excluded from availability everywhere, so it
      // reads neutral — never as an outage (spec 2026-08-18-10).
      return {
        color: "bg-status-neutral",
        barFill: "var(--status-neutral)",
        chartColor: NEUTRAL_CHART,
        badgeVariant: "secondary",
        labelKey: "abandoned",
        bannerSurface:
          "border-border bg-gradient-to-r from-muted/60 via-muted/25 to-transparent",
        bannerTitle: "text-foreground",
        bannerPill: "border-border bg-muted text-muted-foreground",
        isDown: false,
      };
    case "error":
    case "down":
      return {
        color: "bg-status-error",
        barFill: "var(--status-error)",
        chartColor: DOWN_CHART,
        badgeVariant: "destructive",
        labelKey: "outage",
        bannerSurface: "border-status-error/30 bg-gradient-to-r from-status-error/[0.12] via-status-error/[0.05] to-transparent",
        bannerTitle: "text-status-error-foreground",
        bannerPill: "border-status-error/30 bg-status-error/15 text-status-error-foreground",
        isDown: true,
      };
    // Page-level rollup only (spec 2026-08-08-05) — no individual check ever
    // reports this status itself, but the page-level overallStatus does when
    // a resource is inside an active maintenance window. Blue/info, distinct
    // from the amber warning/degraded treatment: maintenance is planned, not
    // a fault.
    case "maintenance":
      return {
        color: "bg-primary",
        barFill: "var(--primary)",
        chartColor: NEUTRAL_CHART,
        badgeVariant: "info",
        labelKey: "underMaintenance",
        bannerSurface: "border-primary/25 bg-gradient-to-r from-primary/[0.10] via-primary/[0.04] to-transparent",
        bannerTitle: "text-primary",
        bannerPill: "border-primary/25 bg-primary/10 text-primary",
        isDown: false,
      };
    default:
      // Also covers the page-level "unknown" rollup (spec 2026-08-08-05,
      // no resource has a usable status yet) — same muted/gray treatment as
      // an individual resource with no live data. The hero renders its own
      // "Status Unknown" label text; this default's labelKey stays
      // "unknown" for the per-resource case.
      return {
        color: "bg-status-neutral",
        barFill: "var(--status-neutral)",
        chartColor: NEUTRAL_CHART,
        badgeVariant: "secondary",
        labelKey: "unknown",
        bannerSurface: "border-border bg-gradient-to-r from-muted/60 via-muted/25 to-transparent",
        bannerTitle: "text-foreground",
        bannerPill: "border-border bg-muted text-muted-foreground",
        isDown: false,
      };
  }
}
