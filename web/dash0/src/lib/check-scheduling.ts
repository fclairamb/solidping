import type { Check, CheckTypeInfo } from "@/api/hooks";
import { hmsToSeconds, secondsToHMS } from "@/components/shared/check-form";

/**
 * The scheduling page's math, kept out of the component so it can be proved.
 *
 * Everything here mirrors the server's own demand formula
 * (`server/internal/entitlements/check_rate.go`, `checksPerMinuteRate`): the
 * page must never show a total that disagrees with the cap the backend
 * enforces, which is what would happen if it invented a second formula.
 */

/**
 * The period ladder the check form offers (`buildIntervalOptions` in
 * `@/components/shared/check-form`), in seconds.
 *
 * Kept here as numbers rather than "HH:MM:SS" strings because every
 * calculation on this page is arithmetic. `check-scheduling.test.ts` asserts
 * this ladder still equals the form's, so the two cannot drift apart silently.
 */
export const PERIOD_STEP_SECONDS = [
  5, 10, 30, 60, 300, 600, 1800, 3600, 21600, 43200, 86400, 604800, 1209600,
  2592000,
] as const;

/**
 * Passive check types, mirroring `checkerdef.CheckType.IsPassive()` in
 * `server/internal/checkers/checkerdef/types.go`.
 *
 * A passive check is driven by an inbound signal (an HTTP heartbeat ping, an
 * incoming email) and returns before the per-org rate gate, so it consumes no
 * execution budget and can never be the reason an org is throttled. It is
 * therefore excluded from this page entirely — listing it would invite users
 * to slow down checks that cost them nothing.
 */
export function isPassiveCheckType(type?: string | null): boolean {
  return type === "heartbeat" || type === "email";
}

/**
 * A single check's contribution to the org's per-minute execution demand.
 *
 * Each selected region runs the check at the FULL period (spec 2026-07-20-05),
 * so multi-region multiplies the cost. A check with no region still runs once,
 * hence `max(1, regions)` — the same clamp the server applies.
 */
export function contributionPerMinute(
  regionCount: number,
  periodSeconds: number,
): number {
  if (!Number.isFinite(periodSeconds) || periodSeconds <= 0) {
    return 0;
  }

  return (Math.max(1, regionCount) * 60) / periodSeconds;
}

/** A check as this page edits it: its saved state plus the user's draft. */
export interface SchedulingRow {
  uid: string;
  name: string;
  type: string;
  regions: string[];
  /** Saved period, in seconds. */
  currentPeriodSeconds: number;
  /** Saved enabled flag. */
  currentEnabled: boolean;
  /** Draft period, in seconds — what the meter counts. */
  periodSeconds: number;
  /** Draft enabled flag. */
  enabled: boolean;
  /** Shortest period this check type allows. */
  minPeriodSeconds: number;
  /** Longest period this check type allows; 0 means unbounded. */
  maxPeriodSeconds: number;
}

/** What a row costs per minute right now, in the user's draft. */
export function rowContribution(row: SchedulingRow): number {
  if (!row.enabled) {
    return 0;
  }

  return contributionPerMinute(row.regions.length, row.periodSeconds);
}

/** What a row costs per minute as currently saved on the server. */
export function savedRowContribution(row: SchedulingRow): number {
  if (!row.currentEnabled) {
    return 0;
  }

  return contributionPerMinute(row.regions.length, row.currentPeriodSeconds);
}

/** Org demand for the draft the user is looking at. */
export function totalDemand(rows: SchedulingRow[]): number {
  return rows.reduce((sum, row) => sum + rowContribution(row), 0);
}

/** Org demand as saved — the figure the server's own payload reports. */
export function savedTotalDemand(rows: SchedulingRow[]): number {
  return rows.reduce((sum, row) => sum + savedRowContribution(row), 0);
}

/** True when the user has changed either editable field on this row. */
export function isRowDirty(row: SchedulingRow): boolean {
  return (
    row.periodSeconds !== row.currentPeriodSeconds ||
    row.enabled !== row.currentEnabled
  );
}

const DEFAULT_MIN_PERIOD_SECONDS = 5;

/**
 * Turns the checks list into editable rows, dropping what this page must not
 * show: passive types (no execution cost) and internal checks (not the
 * customer's to schedule — and excluded from the server's demand figure too).
 *
 * Disabled checks are KEPT: re-enabling one is the other honest lever on this
 * page, and hiding them would make the total jump for no visible reason when
 * the user flips one on.
 */
export function buildSchedulingRows(
  checks: Check[],
  typeInfo: Map<string, CheckTypeInfo>,
): { rows: SchedulingRow[]; passiveCount: number } {
  const rows: SchedulingRow[] = [];
  let passiveCount = 0;

  for (const check of checks) {
    if (check.internal) {
      continue;
    }

    if (isPassiveCheckType(check.type)) {
      passiveCount += 1;
      continue;
    }

    const info = typeInfo.get(check.type ?? "");
    const periodSeconds = check.period ? hmsToSeconds(check.period) : 0;

    rows.push({
      uid: check.uid,
      name: check.name || check.slug || check.uid,
      type: check.type ?? "",
      regions: check.regions ?? [],
      currentPeriodSeconds: periodSeconds,
      currentEnabled: check.enabled !== false,
      periodSeconds,
      enabled: check.enabled !== false,
      minPeriodSeconds: info?.minPeriodSeconds || DEFAULT_MIN_PERIOD_SECONDS,
      maxPeriodSeconds: info?.maxPeriodSeconds || 0,
    });
  }

  // Heaviest first: the page is a "get under the cap" tool, so the checks
  // costing the most must be the ones the eye lands on — and disabled checks,
  // which cost nothing, sink to the bottom. Ties break on uid so the order is
  // stable across renders and identical across sessions.
  rows.sort((a, b) => {
    const delta = savedRowContribution(b) - savedRowContribution(a);

    return delta !== 0 ? delta : a.uid.localeCompare(b.uid);
  });

  return { rows, passiveCount };
}

/** Steps this row may be set to, honoring its type's min/max constraints. */
export function allowedStepsFor(row: {
  minPeriodSeconds: number;
  maxPeriodSeconds: number;
}): number[] {
  return PERIOD_STEP_SECONDS.filter(
    (step) =>
      step >= row.minPeriodSeconds &&
      (row.maxPeriodSeconds === 0 || step <= row.maxPeriodSeconds),
  );
}

export interface PeriodOption {
  seconds: number;
  /**
   * True for the synthetic entry describing a period that is not one of the
   * ladder's steps. A check can hold any period the API accepts (an import, an
   * API caller, a future form), and a select that silently omits the current
   * value would show the wrong period and rewrite it on the first save.
   */
  custom: boolean;
}

/**
 * Options for one row's period select: the allowed steps, plus the row's own
 * period as a leading `custom` entry when it is not a step.
 *
 * Local to this page on purpose — spec 2026-08-26-05 owns the same treatment
 * for the check form's dropdown, and doing it there is not this spec's job.
 */
export function periodOptionsFor(row: SchedulingRow): PeriodOption[] {
  const steps = allowedStepsFor(row);
  const options: PeriodOption[] = steps.map((seconds) => ({
    seconds,
    custom: false,
  }));

  if (row.periodSeconds > 0 && !steps.includes(row.periodSeconds)) {
    options.unshift({ seconds: row.periodSeconds, custom: true });
  }

  return options;
}

/** The next longer allowed step, or null when the row is already at its longest. */
export function nextLongerStep(row: SchedulingRow): number | null {
  const steps = allowedStepsFor(row);

  for (const step of steps) {
    if (step > row.periodSeconds) {
      return step;
    }
  }

  return null;
}

export interface RebalanceProposal {
  /** uid → proposed period in seconds. Only rows that actually move appear. */
  proposals: Map<string, number>;
  /** Demand once the proposals are applied. */
  totalAfter: number;
  /**
   * True when the proposal actually lands the org at or under its cap. False
   * means every check is already at its longest allowed period and the demand
   * still exceeds the cap — disabling checks or upgrading are then the only
   * remedies, and the UI must say so rather than pretend it fixed something.
   */
  reachedLimit: boolean;
}

/**
 * Proposes longer periods that bring the org back under its cap.
 *
 * Greedy and deterministic: at each step it stretches the single heaviest
 * contributor that still has a longer step available, one ladder step at a
 * time, and stops as soon as the total is at or under the cap. Ties are broken
 * on uid ascending, so the same input always yields the same proposal —
 * including when the input arrives in a different order.
 *
 * "Largest contributors first" is what makes it feel fair: the check running
 * every 5 seconds across four regions is what is eating the budget, and
 * stretching it hurts the org's coverage far less than stretching twenty
 * hourly checks would.
 *
 * Disabled rows are left alone — the proposal must never turn a check the user
 * switched off back on, and a disabled check contributes nothing anyway.
 */
export function proposeRebalance(
  rows: SchedulingRow[],
  limit: number | null | undefined,
): RebalanceProposal {
  const working = rows.map((row) => ({ ...row }));

  if (limit === null || limit === undefined) {
    // Unlimited: there is nothing to bring anything under.
    return {
      proposals: new Map(),
      totalAfter: totalDemand(working),
      reachedLimit: true,
    };
  }

  // Bounded by construction: each iteration moves exactly one row strictly up
  // its own finite ladder, so it cannot exceed rows × steps moves.
  const maxIterations = working.length * PERIOD_STEP_SECONDS.length + 1;
  let reachedLimit = totalDemand(working) <= limit;

  for (let i = 0; i < maxIterations && !reachedLimit; i++) {
    let victim: (typeof working)[number] | null = null;
    let victimNext = 0;
    let victimCost = -1;

    for (const row of working) {
      if (!row.enabled) {
        continue;
      }

      const next = nextLongerStep(row);
      if (next === null) {
        continue;
      }

      const cost = rowContribution(row);
      if (
        cost > victimCost ||
        (cost === victimCost && victim !== null && row.uid < victim.uid)
      ) {
        victim = row;
        victimNext = next;
        victimCost = cost;
      }
    }

    if (victim === null) {
      break;
    }

    victim.periodSeconds = victimNext;
    reachedLimit = totalDemand(working) <= limit;
  }

  const proposals = new Map<string, number>();
  for (let i = 0; i < working.length; i++) {
    if (working[i].periodSeconds !== rows[i].periodSeconds) {
      proposals.set(working[i].uid, working[i].periodSeconds);
    }
  }

  return { proposals, totalAfter: totalDemand(working), reachedLimit };
}

/**
 * Anchors the meter on the server's own demand figure, then applies the delta
 * the user's unsaved edits would make.
 *
 * The saved total MUST be the server's `checksPerMinute.demand` (spec
 * 2026-08-26-03) rather than a second client-side sum: that is the number the
 * over-limit banner on this very page quotes, and the number the rate gate
 * actually enforces. Two independently-computed totals sitting side by side
 * would eventually disagree — over a paginated list still loading, over an
 * internal check the client cannot see, over any future change to the server's
 * scope — and the page would then argue with itself.
 *
 * The DELTA is client-side because it has to be: it describes edits that do
 * not exist server-side yet. Both halves use the same formula, so the delta is
 * exact for every check the page can see.
 *
 * Falls back to the client sum when the server sent no figure at all.
 */
export function anchoredDemand(
  serverDemand: number | null | undefined,
  clientSaved: number,
  clientDraft: number,
): { saved: number; draft: number } {
  const saved =
    serverDemand === null || serverDemand === undefined
      ? clientSaved
      : serverDemand;

  return { saved, draft: Math.max(0, saved + (clientDraft - clientSaved)) };
}

/**
 * Formats a per-minute rate the way the rest of the product does: whole
 * numbers stay whole, fractional rates (a check every 5 minutes contributes
 * 0.2) keep one decimal. Mirrors `formatCheckRateDemand`.
 */
export function formatRate(rate: number): string {
  return Number.isInteger(rate) ? String(rate) : rate.toFixed(1);
}

export type PeriodUnitKey = "seconds" | "minutes" | "hours" | "days" | "weeks";

/**
 * Breaks a period into the largest whole unit that divides it evenly, so the
 * component can render it through a plural-aware translation key instead of
 * hard-coded English.
 */
export function describePeriod(seconds: number): {
  unit: PeriodUnitKey;
  count: number;
} {
  if (seconds > 0 && seconds % 604800 === 0) {
    return { unit: "weeks", count: seconds / 604800 };
  }
  if (seconds > 0 && seconds % 86400 === 0) {
    return { unit: "days", count: seconds / 86400 };
  }
  if (seconds > 0 && seconds % 3600 === 0) {
    return { unit: "hours", count: seconds / 3600 };
  }
  if (seconds > 0 && seconds % 60 === 0) {
    return { unit: "minutes", count: seconds / 60 };
  }

  return { unit: "seconds", count: seconds };
}

/** The wire format the checks API expects for a period. */
export function periodToHMS(seconds: number): string {
  return secondsToHMS(seconds);
}
