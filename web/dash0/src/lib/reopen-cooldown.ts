// reopen-cooldown computes the ACTUAL reopen-cooldown window (spec
// 2026-08-24-05) from the check form's Reopen Cooldown Multiplier field,
// mirroring the backend's calculateCooldown clamp exactly
// (server/internal/handlers/incidents/service.go) so the form's hint never
// promises a window the backend won't actually honor. Before this, the field
// only asked for a bare N and left "min 2 min, max 30 min" as prose — a user
// who set 60 on a 1-minute check silently got a 30-minute window with no
// on-screen warning.

import type { TFunction } from "i18next";
import { formatDuration } from "./period-estimate";

export const REOPEN_COOLDOWN_MIN_SECONDS = 120; // 2 min floor
export const REOPEN_COOLDOWN_MAX_SECONDS = 1800; // 30 min cap

export interface ReopenCooldownResult {
  /** The window the backend will actually use. 0 = reopening disabled. */
  seconds: number;
  /** Unclamped multiplier × period — only meaningful when clamp !== "none". */
  rawSeconds: number;
  clamp: "none" | "capped" | "floored";
}

/**
 * calculateReopenCooldownSeconds mirrors calculateCooldown in
 * server/internal/handlers/incidents/service.go: multiplier 0 disables
 * reopening entirely (seconds: 0); otherwise raw = multiplier × periodSeconds,
 * clamped to [REOPEN_COOLDOWN_MIN_SECONDS, REOPEN_COOLDOWN_MAX_SECONDS].
 */
export function calculateReopenCooldownSeconds(
  multiplier: number,
  periodSeconds: number,
): ReopenCooldownResult {
  if (!Number.isFinite(multiplier) || multiplier <= 0) {
    return { seconds: 0, rawSeconds: 0, clamp: "none" };
  }

  const raw = multiplier * Math.max(0, periodSeconds);

  if (raw < REOPEN_COOLDOWN_MIN_SECONDS) {
    return { seconds: REOPEN_COOLDOWN_MIN_SECONDS, rawSeconds: raw, clamp: "floored" };
  }
  if (raw > REOPEN_COOLDOWN_MAX_SECONDS) {
    return { seconds: REOPEN_COOLDOWN_MAX_SECONDS, rawSeconds: raw, clamp: "capped" };
  }

  return { seconds: raw, rawSeconds: raw, clamp: "none" };
}

/**
 * describeReopenCooldown renders the translated computed hint for the Reopen
 * Cooldown Multiplier field:
 *   multiplier 0        -> "off — always opens a new incident"
 *   normal               -> "= 5 min"
 *   capped (> 30 min)    -> "= 30 min (capped, from 60 min)"
 *   floored (< 2 min)    -> "= 2 min (floor, from 10 s)"
 */
export function describeReopenCooldown(
  multiplier: number,
  periodSeconds: number,
  t: TFunction,
): string {
  if (!Number.isFinite(multiplier) || multiplier <= 0) {
    return t("form.reopenCooldownOff");
  }

  const result = calculateReopenCooldownSeconds(multiplier, periodSeconds);
  const duration = formatDuration(result.seconds);

  if (result.clamp === "capped") {
    return t("form.reopenCooldownCapped", { duration, raw: formatDuration(result.rawSeconds) });
  }
  if (result.clamp === "floored") {
    return t("form.reopenCooldownFloored", { duration, raw: formatDuration(result.rawSeconds) });
  }

  return t("form.periodEstimate", { duration });
}
