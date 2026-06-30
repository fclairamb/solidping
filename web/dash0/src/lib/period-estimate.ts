// period-estimate describes a Confirmation / Recovery window (stored in seconds)
// in human terms for the check form, WITHOUT changing the stored/wire unit.
//
// The incident state machine is genuinely wall-clock based (it compares
// `now - FirstFailureAt` against the window), and probe delivery in this
// distributed prober is jittery, so the probe count is only ever an *estimate*
// (leading `≈`). Passive checks (heartbeat / email) have no real probe cadence,
// so they get the duration only — no count.

import type { TFunction } from "i18next";

export type PeriodKind = "confirmation" | "recovery";

// formatDuration renders a whole-second window as a compact human duration:
//   120  -> "2 min"
//   90   -> "1 min 30 s"
//   3600 -> "1 h"
//   3661 -> "1 h 1 min 1 s"
// Units are kept locale-neutral (h / min / s) so the duration reads the same in
// every language; the surrounding sentence is translated.
export function formatDuration(seconds: number): string {
  const total = Math.max(0, Math.round(seconds));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;

  const parts: string[] = [];
  if (h > 0) parts.push(`${h} h`);
  if (m > 0) parts.push(`${m} min`);
  if (s > 0) parts.push(`${s} s`);
  // total === 0 is handled by the caller (immediate copy); guard anyway.
  if (parts.length === 0) parts.push(`${total} s`);
  return parts.join(" ");
}

// describePeriod returns the muted estimate line rendered under a Confirmation /
// Recovery seconds input, or "" when there is nothing useful to show.
//
//   windowSeconds === 0 -> the "immediate" copy for the kind
//     (confirmation: opens an incident immediately on the first failure;
//      recovery: resolves the incident immediately on the first success).
//   otherwise -> the human duration, and for ACTIVE checks with a real cadence
//     (intervalSeconds > 0) the approximate probe count at that interval,
//     count = max(1, round(windowSeconds / intervalSeconds)) so a window > 0 is
//     never shown as "0 checks".
//
// `t` is the i18next checks-namespace translator; the count phrase uses the
// i18n plural form so "1 check" vs "N checks" is correct per locale.
export function describePeriod(
  windowSeconds: number,
  intervalSeconds: number,
  kind: PeriodKind,
  t: TFunction,
): string {
  if (!Number.isFinite(windowSeconds) || windowSeconds < 0) return "";

  if (windowSeconds === 0) {
    return kind === "confirmation"
      ? t("form.periodImmediateOpen")
      : t("form.periodImmediateResolve");
  }

  const duration = t("form.periodEstimate", { duration: formatDuration(windowSeconds) });

  // Only active checks with a real polling cadence get a probe-count estimate.
  if (!Number.isFinite(intervalSeconds) || intervalSeconds <= 0) {
    return duration;
  }

  const count = Math.max(1, Math.round(windowSeconds / intervalSeconds));
  const checks = t("form.periodEstimateChecks", {
    count,
    interval: formatDuration(intervalSeconds),
  });
  return `${duration} ${checks}`;
}
