import type { DashboardWindow } from "@/lib/preferences-sync";

const HOUR_MS = 3_600_000;

// Cell geometry of the "Checks at a glance" strips for each dashboard window.
// The strips follow the window picked in the header (24h / 7d / 30d), like the
// availability tile and banner. Every width divides a UTC day, so the server's
// cell alignment (truncation relative to the zero instant) lands on the same
// boundaries as the epoch-based alignment below.
const GLANCE_STRIP_GEOMETRY: Record<DashboardWindow, { cells: number; bucketMs: number }> = {
  "24h": { cells: 24, bucketMs: HOUR_MS },
  "7d": { cells: 28, bucketMs: 6 * HOUR_MS },
  "30d": { cells: 30, bucketMs: 24 * HOUR_MS },
};

export interface GlanceStripWindow {
  from: string;
  to: string;
  bucketSeconds: number;
}

/** The [from, to) range and cell width of the glance strips for `period`, ending
 * with the cell that holds `nowMs` (current cell included), so the server
 * returns exactly `cells` aligned cells. */
export function glanceStripWindow(period: DashboardWindow, nowMs: number): GlanceStripWindow {
  const { cells, bucketMs } = GLANCE_STRIP_GEOMETRY[period] ?? GLANCE_STRIP_GEOMETRY["24h"];
  const cellStart = Math.floor(nowMs / bucketMs) * bucketMs;
  return {
    from: new Date(cellStart - (cells - 1) * bucketMs).toISOString(),
    to: new Date(cellStart + bucketMs).toISOString(),
    bucketSeconds: bucketMs / 1000,
  };
}
