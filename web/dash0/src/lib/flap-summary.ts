// flap-summary builds the interpolation values for the check-detail
// "Flapping: outage #N within <window> — required recovery ×M = <effective>"
// line (spec 2026-08-24-05), rendered when Check.flapState is present.

import type { Check } from "@/api/hooks";
import { formatDuration } from "./period-estimate";

export interface FlappingSummaryParams {
  /** 1-indexed outage position within the rolling window. flapState.flapCount
   * is 0-indexed (0 = first flap, i.e. the 2nd outage overall — see
   * models.Check.FlapCount on the backend), so count = flapCount + 1. */
  count: number;
  window: string;
  /** Undefined when there is no nonzero base recovery period to multiply
   * (an immediate/0s recovery period makes the backoff factor a no-op). */
  multiplier?: number;
  effective: string;
}

/**
 * flappingSummaryParams computes the check-detail flapping line's
 * interpolation values, or undefined when the check has no live flap state
 * to report (flapState absent — see the backend's buildFlapStateResponse:
 * omitted when the feature is off or nothing has accumulated).
 */
export function flappingSummaryParams(check: Check): FlappingSummaryParams | undefined {
  const flapState = check.flapState;
  if (!flapState) return undefined;

  const window = check.flappingWindowSeconds
    ? formatDuration(check.flappingWindowSeconds)
    : "";
  const effective = formatDuration(flapState.effectiveRecoveryPeriodSeconds);
  const base = check.recoveryPeriodSeconds ?? 0;

  return {
    count: flapState.flapCount + 1,
    window,
    effective,
    // Only meaningful when there is a nonzero base to multiply — with an
    // immediate (0s) recovery period the backoff factor has nothing to act
    // on, so the multiplier clause is dropped rather than showing ×NaN.
    multiplier:
      base > 0
        ? Math.max(1, Math.round(flapState.effectiveRecoveryPeriodSeconds / base))
        : undefined,
  };
}
