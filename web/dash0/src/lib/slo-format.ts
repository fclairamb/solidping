import type { SloState, SloStatusRow } from "@/api/hooks";

/**
 * Renders an attainment percentage.
 *
 * `null` means the window carried no countable probe — the availability API's
 * long-standing "no data is not 100%" rule. It MUST render as a dash, never as
 * a number: showing 100% for a window nobody was watching is the single most
 * dangerous thing this page could do.
 */
export function formatAttainment(pct: number | null | undefined): string | null {
  if (pct === null || pct === undefined) {
    return null;
  }
  // Three decimals: an SLO conversation is about the difference between 99.9
  // and 99.95, which one decimal cannot express.
  return `${pct.toFixed(3)}%`;
}

/** Renders a target percentage, trimming trailing zeros (99.900 -> 99.9). */
export function formatTarget(pct: number): string {
  return `${Number(pct.toFixed(3))}%`;
}

/**
 * Renders a duration in seconds compactly. Negative input renders with a
 * leading minus so an overspent error budget reads as an overspend rather than
 * silently clamping to zero.
 */
export function formatBudgetSeconds(seconds: number): string {
  const negative = seconds < 0;
  const abs = Math.abs(Math.round(seconds));

  const days = Math.floor(abs / 86400);
  const hours = Math.floor((abs % 86400) / 3600);
  const minutes = Math.floor((abs % 3600) / 60);
  const secs = abs % 60;

  let out: string;
  if (days > 0) {
    out = `${days}d ${hours}h`;
  } else if (hours > 0) {
    out = `${hours}h ${minutes}m`;
  } else if (minutes > 0) {
    out = `${minutes}m ${secs}s`;
  } else {
    out = `${secs}s`;
  }

  return negative ? `-${out}` : out;
}

/** Fraction of the budget still available, clamped to [0, 1] for a progress bar. */
export function budgetRemainingFraction(row: SloStatusRow): number {
  if (row.budgetTotalSeconds <= 0) {
    return row.budgetConsumedSeconds > 0 ? 0 : 1;
  }
  const fraction = row.budgetRemainingSeconds / row.budgetTotalSeconds;
  return Math.min(1, Math.max(0, fraction));
}

export type SloStateTone = "healthy" | "warning" | "danger" | "neutral";

export function sloStateTone(state: SloState): SloStateTone {
  switch (state) {
    case "healthy":
      return "healthy";
    case "at_risk":
      return "warning";
    case "breached":
      return "danger";
    default:
      return "neutral";
  }
}

/** Tailwind classes for the state chip, matching the status-badge palette. */
export function sloStateBadgeClass(state: SloState): string {
  switch (state) {
    case "healthy":
      return "border-transparent bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300";
    case "at_risk":
      return "border-transparent bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300";
    case "breached":
      return "border-transparent bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300";
    default:
      return "border-transparent bg-muted text-muted-foreground";
  }
}

/** Bar color for the remaining-budget meter. */
export function sloBudgetBarClass(state: SloState): string {
  switch (state) {
    case "healthy":
      return "bg-emerald-500";
    case "at_risk":
      return "bg-amber-500";
    case "breached":
      return "bg-red-500";
    default:
      return "bg-muted-foreground/40";
  }
}
