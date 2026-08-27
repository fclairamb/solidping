/**
 * The single client-side mapping from a bucket's availability to a colour.
 *
 * The classification itself is the SERVER's — `uptimebar.Classify`
 * (server/internal/uptimebar/classify.go), which the public status page, the
 * badge uptime bar and the check-detail availability strip all share, including
 * the small-bucket calibration guard (a bucket with exactly one failed sample is
 * never painted red). So the wire already carries a `status`; this module only
 * turns that word into Tailwind classes.
 *
 * `classifyAvailability` is the local fallback for the one caller that has a
 * percentage but no server-side status yet (the dashboard's 24h uptime strip,
 * which is derived from already-fetched result rows). It reproduces the Go rule
 * exactly rather than inventing a fourth green/amber/red mapping — the spec's
 * whole complaint was that four surfaces disagreed.
 */

/** The shared wire vocabulary. Mirrors uptimebar's Status* constants. */
export type AvailabilityStatus = "up" | "degraded" | "down" | "noData";

/** Platform default thresholds — models.DefaultAvailabilityThreshold{Up,Degraded}. */
export const AVAILABILITY_THRESHOLD_UP = 99.9;
export const AVAILABILITY_THRESHOLD_DEGRADED = 99.0;

/**
 * The TypeScript twin of `uptimebar.Classify`.
 *
 * `failures` is total − successful. A bucket with no countable probes must be
 * passed `hasData: false` (or `pct: undefined`) — "no data" is a distinct third
 * state and is never 100%.
 */
export function classifyAvailability(
  pct: number | null | undefined,
  failures?: number,
  upThreshold = AVAILABILITY_THRESHOLD_UP,
  degradedThreshold = AVAILABILITY_THRESHOLD_DEGRADED,
): AvailabilityStatus {
  if (pct === null || pct === undefined || Number.isNaN(pct)) return "noData";
  if (pct >= upThreshold) return "up";
  if (pct >= degradedThreshold) return "degraded";
  // Small-bucket calibration guard: red needs at least two failed samples, so a
  // single failed minute never turns a whole hour red.
  //
  // `undefined` means the caller could not count the failures, NOT zero — with
  // zero the guard would fire on every bucket and nothing would ever be red.
  // Skipping it there keeps the percentage thresholds honest and leaves the
  // guard to the callers that actually know (the server, and any client with
  // total/successful counts).
  if (failures !== undefined && failures <= 1) return "degraded";
  return "down";
}

/** Solid cell background for a strip. */
export function availabilityCellClass(status: AvailabilityStatus): string {
  switch (status) {
    case "up":
      return "bg-status-ok";
    case "degraded":
      return "bg-status-warning";
    case "down":
      return "bg-status-error";
    case "noData":
      return "bg-muted-foreground/30";
  }
}

/** Smaller dot used inside tooltips / legends. */
export function availabilityDotClass(status: AvailabilityStatus): string {
  switch (status) {
    case "up":
      return "bg-status-ok/75";
    case "degraded":
      return "bg-status-warning/75";
    case "down":
      return "bg-status-error/75";
    case "noData":
      return "bg-muted-foreground/50";
  }
}

/** i18n key under the `checks` namespace for a status word. */
export function availabilityStatusLabelKey(status: AvailabilityStatus): string {
  return `detail.availabilityStrip.status.${status}`;
}

/**
 * The canonical availability percentage format: exactly 100 stays "100%",
 * anything at or above 99 gets two decimals (99.9% and 99.0% are a factor of
 * ten apart in downtime and must not collapse to the same string), everything
 * below gets one.
 *
 * The two-decimal band is also what stops a near-perfect number from ROUNDING UP
 * to "100%" — 99.95% is not 100%, and the whole spec is about not manufacturing
 * that claim.
 */
export function formatAvailabilityNumber(pct: number): string {
  if (pct >= 100) return "100%";
  if (pct >= 99) return `${pct.toFixed(2)}%`;
  return `${pct.toFixed(1)}%`;
}

/**
 * `formatAvailabilityNumber` for a value that may be absent. Returns `null` —
 * never "0%" and never "100%" — so the caller renders its own "no data" label
 * for the third state.
 */
export function formatAvailabilityPct(
  pct: number | null | undefined,
): string | null {
  if (pct === null || pct === undefined || Number.isNaN(pct)) return null;
  return formatAvailabilityNumber(pct);
}
