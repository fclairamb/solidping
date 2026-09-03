/**
 * Pure derivations behind TV mode (spec 2026-08-29-08).
 *
 * All of it lives here rather than inside the board component for one reason:
 * a wallboard's whole job is to be believed from four metres away, and every
 * rule below is a way of being wrong in a way nobody in the room would notice.
 * Pure functions can be tested against the exact minute an incident opens.
 */

import type { PublicIncident, StatusPage } from "@/api/hooks";

/**
 * The ambient state the whole board is tinted by. `stale` is not a page state
 * the server can report — it is what the board falls back to when it has
 * stopped hearing from the API (see isStale).
 */
export type TvState =
  | "operational"
  | "degraded"
  | "down"
  | "maintenance"
  | "unknown"
  | "stale";

/**
 * Severity floor an active publication imposes on the board.
 *
 * A manually published incident MUST show even while every probe is green:
 * the operator published it precisely because they know something the checks
 * do not (a partner API degrading, a data issue, a migration). Silently
 * rendering "all systems operational" over an open `critical` publication
 * would be the single worst failure this board could have.
 */
function severityFloor(severity: string | undefined): TvState | null {
  switch (severity) {
    case "critical":
      return "down";
    case "major":
    case "minor":
      return "degraded";
    default:
      // Ungraded: the operator set no severity, so we put no words in their
      // mouth beyond "something is open" — which `degraded` already says.
      return "degraded";
  }
}

/** Ordering used to pick the worst of several signals. Higher wins. */
const STATE_RANK: Record<TvState, number> = {
  operational: 0,
  unknown: 1,
  maintenance: 2,
  stale: 3,
  degraded: 4,
  down: 5,
};

/**
 * Resolves the board's ambient state from the page rollup and any open
 * incident publications.
 *
 * Two rules carry the design:
 *
 *   - Active publications ESCALATE but never de-escalate. A red rollup stays
 *     red under a `minor` publication.
 *   - `maintenance` is never escalated INTO by an incident-free page, and a
 *     planned window never paints the office red — it is blue/info, because a
 *     wall of red at 03:00 during a scheduled migration trains everyone to
 *     ignore the screen.
 */
export function resolveTvState(
  rollup: string | undefined,
  activeIncidents: PublicIncident[] | undefined,
): TvState {
  let state = normalizeRollup(rollup);

  for (const incident of activeIncidents ?? []) {
    const floor = severityFloor(incident.severity);
    if (floor && STATE_RANK[floor] > STATE_RANK[state]) {
      state = floor;
    }
  }

  return state;
}

/**
 * How many open publications are the SOLE reason the board is not showing what
 * the server's own rollup says — `0` whenever the checks themselves account
 * for the state.
 *
 * The board escalating past a green rollup is correct and deliberate (an
 * operator published something the probes cannot see). What was not correct was
 * saying "Some Systems Degraded" with no hint of where the amber came from: an
 * operator would open the dashboard, find every check up, and conclude the
 * board was broken. Ten days of that is how a publication came to outlive its
 * incident unnoticed (spec 2026-09-02-05).
 *
 * Deliberately a COUNT rather than a boolean: the subtitle names the number,
 * and "1 open incident" versus "3 open incidents" is the difference between a
 * forgotten entry and a real multi-service event.
 */
export function incidentDrivenCount(
  rollup: string | undefined,
  activeIncidents: PublicIncident[] | undefined,
): number {
  const base = normalizeRollup(rollup);
  const resolved = resolveTvState(rollup, activeIncidents);

  if (STATE_RANK[resolved] <= STATE_RANK[base]) {
    return 0;
  }

  return activeIncidents?.length ?? 0;
}

/** Maps the server's `overallStatus` string onto a board state. */
export function normalizeRollup(rollup: string | undefined): TvState {
  switch (rollup) {
    case "operational":
    case "degraded":
    case "down":
    case "maintenance":
      return rollup;
    default:
      return "unknown";
  }
}

/**
 * STALE_AFTER_MS is how long the board tolerates silence before it stops
 * claiming to know anything: three missed 30 s polls.
 *
 * The threshold is fixed even though the poll interval tightens during an
 * incident — a faster poll must not make the guard hair-triggered on a
 * network that merely hiccups.
 */
export const STALE_AFTER_MS = 90_000;

/**
 * True when the last successful poll is too old to trust.
 *
 * `undefined` (nothing has ever succeeded) is NOT stale: that is the initial
 * load, and the board shows its loading state rather than shouting about data
 * it has not asked for yet.
 */
export function isStale(
  lastSuccessAt: number | undefined,
  now: number,
  thresholdMs: number = STALE_AFTER_MS,
): boolean {
  if (lastSuccessAt === undefined) return false;

  return now - lastSuccessAt >= thresholdMs;
}

/**
 * Poll cadence: 30 s at rest, 15 s once the board is showing anything other
 * than "operational".
 *
 * The asymmetry is the point. A healthy wall panel refreshing twice a minute
 * costs nothing and tells nobody anything new; during an incident the same
 * panel is what a room full of people is staring at, and thirty seconds of
 * staleness is thirty seconds of a resolved outage still reading red.
 */
export function pollIntervalMs(state: TvState): number {
  return state === "operational" ? 30_000 : 15_000;
}

/**
 * Whole days between an incident's resolution and now, floored at 0.
 *
 * Floored rather than allowed to go negative because clocks disagree: a
 * browser a few minutes ahead of the server must read "0 days", never "-1".
 */
export function daysSince(resolvedAt: string, now: number): number | null {
  const then = Date.parse(resolvedAt);
  if (Number.isNaN(then)) return null;

  return Math.max(0, Math.floor((now - then) / 86_400_000));
}

/**
 * The most recent resolution timestamp in a page's incident history, or null
 * when nothing has ever been resolved.
 *
 * Scans rather than trusting the API's ordering: "days since last incident" is
 * a claim about the whole history, and silently reading element 0 would make
 * it a claim about whatever the server happened to return first.
 */
export function lastResolvedAt(
  incidents: PublicIncident[] | undefined,
): string | null {
  let best: string | null = null;
  let bestMs = Number.NEGATIVE_INFINITY;

  for (const incident of incidents ?? []) {
    if (!incident.resolvedAt) continue;

    const parsed = Date.parse(incident.resolvedAt);
    if (Number.isNaN(parsed) || parsed <= bestMs) continue;

    bestMs = parsed;
    best = incident.resolvedAt;
  }

  return best;
}

/**
 * The N most recently resolved incidents, newest first.
 */
export function recentResolved(
  incidents: PublicIncident[] | undefined,
  limit: number,
): PublicIncident[] {
  return (incidents ?? [])
    .filter((incident) => Boolean(incident.resolvedAt))
    .sort(
      (a, b) => Date.parse(b.resolvedAt ?? "") - Date.parse(a.resolvedAt ?? ""),
    )
    .slice(0, limit);
}

/** A duration broken into the units the board renders. */
export interface DurationParts {
  days: number;
  hours: number;
  minutes: number;
}

/**
 * Splits a millisecond duration into days/hours/minutes, clamped at zero.
 *
 * Seconds are deliberately absent: a number that changes every second on a
 * wall panel reads as a stopwatch, and draws the eye away from the state.
 */
export function durationParts(ms: number): DurationParts {
  const total = Math.max(0, Math.floor(ms / 60_000));

  return {
    days: Math.floor(total / 1440),
    hours: Math.floor((total % 1440) / 60),
    minutes: total % 60,
  };
}

/** Elapsed milliseconds between two ISO timestamps, or null when unparseable. */
export function elapsedMs(from: string, to: string | number): number | null {
  const start = Date.parse(from);
  const end = typeof to === "number" ? to : Date.parse(to);

  if (Number.isNaN(start) || Number.isNaN(end)) return null;

  return Math.max(0, end - start);
}

/**
 * Which slice of a list to show when it is too long to fit.
 *
 * More than `perPage` active incidents get CYCLED rather than shrunk: three
 * cards at a third of the size are unreadable from across a room, which is the
 * only place this screen is ever read from. Returns the whole list untouched
 * when it fits, so no timer runs on the common case.
 */
export function cycleWindow<T>(items: T[], perPage: number, tick: number): T[] {
  if (items.length <= perPage) return items;

  const pages = Math.ceil(items.length / perPage);
  const start = (tick % pages) * perPage;

  return items.slice(start, start + perPage);
}

/**
 * One non-operational resource, flattened out of the page's sections so the
 * board can name it.
 *
 * `name` is best-effort and may be empty: the i18n fallback belongs to the
 * component, not here, so this stays a pure derivation with no translator.
 */
export interface FailingResource {
  /** Resource uid — the render key. */
  uid: string;
  /** What the page calls this publicly. */
  name: string;
  /** Owning section, for when two sections both have an "API". */
  section: string;
  /** Raw check status, rendered through statusStyle() for its label. */
  status: string;
  /**
   * RFC3339 instant this resource entered its current status, when the server
   * knows it. Absent for group resources — the board then names the component
   * without a duration rather than inventing one.
   */
  since?: string;
}

/**
 * How loudly a status argues for being named on the wall. Higher sorts first;
 * 0 means "never name it".
 */
function resourceRank(status: string): number {
  switch (status) {
    case "error":
    case "down":
      return 3;
    case "degraded":
    case "warning":
      return 2;
    case "ok":
    case "up":
    case "operational":
      return 0;
    default:
      // "abandoned", "unknown", and any status the server grows later. Worth
      // naming — the board is not green and this resource is not claiming to
      // be fine — but always beneath a real failure.
      return 1;
  }
}

/**
 * The resources responsible for a non-green board, worst first.
 *
 * This exists because the board's ambient colour and its explanation come from
 * two DIFFERENT sources that move at different speeds. `overallStatus` is
 * recomputed from live check data on every poll, so the screen turns red the
 * instant a probe fails; `activeIncidents` are publications, which auto-publish
 * only after the page's `autoPublishDelaySeconds` (and never at all when
 * auto-publish is off). In that gap the old board showed a full red screen
 * whose only text was "N days since the last incident" — a flat contradiction,
 * and useless to the person who just looked up at it. Naming the failing checks
 * closes the gap with data the board already has in hand.
 *
 * Checks inside a maintenance window are deliberately excluded: they are down
 * on purpose, and "Database — Outage" on the wall during a migration everyone
 * agreed to is how a wallboard loses its audience.
 */
export function failingResources(
  page: StatusPage | undefined,
): FailingResource[] {
  const ranked: Array<{ rank: number; resource: FailingResource }> = [];

  for (const section of page?.sections ?? []) {
    for (const resource of section.resources ?? []) {
      if (resource.check?.inMaintenance) continue;

      const status = resource.check?.status ?? "unknown";
      const rank = resourceRank(status);
      if (rank === 0) continue;

      ranked.push({
        rank,
        resource: {
          uid: resource.uid,
          name: resource.publicName || resource.check?.name || "",
          section: section.name,
          status,
          since: resource.check?.statusChangedAt,
        },
      });
    }
  }

  // Worst first. Ties keep the page's own section/resource ordering — Array
  // .sort is stable, so this is the order the operator arranged and the order
  // the ordinary public page renders them in.
  return ranked.sort((a, b) => b.rank - a.rank).map((entry) => entry.resource);
}
