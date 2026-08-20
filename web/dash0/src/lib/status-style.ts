// Single source of truth for how a check / result status is rendered in the
// dash0 operator UI. Every status surface (badges, dashboards, timelines,
// summary cards, charts) routes through statusStyle() so the warning/degraded
// amber treatment — and every other status colour — stays consistent.
//
// Two distinct amber states are handled here:
//   - "warning"  — the live current status ("up, but something to report").
//   - "degraded" — the aggregated rollup status (a window had warning(s) but
//                  no dominating failure).
// Both render amber; they differ only in label ("Warning" vs "Degraded").

export type BadgeVariant =
  | "success"
  | "warning"
  | "destructive"
  | "secondary";

export interface StatusStyle {
  // Tailwind background class for solid swatches (timeline bars, status dots).
  color: string;
  // Lighter background class for hover dots / secondary indicators.
  dotColor: string;
  // Recharts / inline SVG fill colour (hex/hsl), neutral when not down.
  chartColor: string;
  // Badge variant from components/ui/badge.tsx.
  badgeVariant: BadgeVariant;
  // i18n key under the "checks" namespace status.* (e.g. status.warning).
  labelKey: string;
  // English fallback label.
  defaultLabel: string;
  // True when this status should read as a hard failure (down/error/timeout).
  isDown: boolean;
}

const NEUTRAL_CHART = "transparent";
const DOWN_CHART = "#ef4444"; // red-500
const WARNING_CHART = "#facc15"; // yellow-400

export function statusStyle(status: string | undefined | null): StatusStyle {
  switch (status) {
    case "up":
    case "ok":
      return {
        color: "bg-status-ok",
        dotColor: "bg-status-ok/75",
        chartColor: NEUTRAL_CHART,
        badgeVariant: "success",
        labelKey: "status.up",
        defaultLabel: "Up",
        isDown: false,
      };
    case "warning":
      return {
        color: "bg-status-warning",
        dotColor: "bg-status-warning/75",
        chartColor: WARNING_CHART,
        badgeVariant: "warning",
        labelKey: "status.warning",
        defaultLabel: "Warning",
        isDown: false,
      };
    case "degraded":
      return {
        color: "bg-status-warning",
        dotColor: "bg-status-warning/75",
        chartColor: WARNING_CHART,
        badgeVariant: "warning",
        labelKey: "status.degraded",
        defaultLabel: "Degraded",
        isDown: false,
      };
    case "validating":
      return {
        color: "bg-status-warning",
        dotColor: "bg-status-warning/75",
        chartColor: WARNING_CHART,
        badgeVariant: "warning",
        labelKey: "status.validating",
        defaultLabel: "Validating",
        isDown: false,
      };
    case "abandoned":
      // Server-minted by the abandoned-result reaper: nothing was ever
      // reported for this attempt because OUR side died, not because the
      // target was down. Excluded from every availability calculation, so it
      // must read as a neutral "not counted" state — never red, never
      // isDown (spec 2026-08-18-10).
      return {
        color: "bg-gray-300",
        dotColor: "bg-gray-400",
        chartColor: NEUTRAL_CHART,
        badgeVariant: "secondary",
        labelKey: "status.abandoned",
        defaultLabel: "Abandoned",
        isDown: false,
      };
    case "down":
    case "error":
    case "timeout":
      return {
        color: "bg-status-error",
        dotColor: "bg-status-error/75",
        chartColor: DOWN_CHART,
        badgeVariant: "destructive",
        labelKey: "status.down",
        defaultLabel: "Down",
        isDown: true,
      };
    default:
      // created, unknown, or any future value.
      return {
        color: "bg-gray-300",
        dotColor: "bg-gray-400",
        chartColor: NEUTRAL_CHART,
        badgeVariant: "secondary",
        labelKey: "status.unknown",
        defaultLabel: status ?? "Unknown",
        isDown: false,
      };
  }
}
