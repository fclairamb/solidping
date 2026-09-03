import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Activity,
  AlertTriangle,
  CheckCircle2,
  HelpCircle,
  WifiOff,
  Wrench,
  XCircle,
} from "lucide-react";
import type { PublicIncident, StatusPage } from "@/api/hooks";
import { severityStyle, statusStyle } from "@/lib/status-style";
import {
  cycleWindow,
  daysSince,
  durationParts,
  elapsedMs,
  failingResources,
  incidentDrivenCount,
  lastResolvedAt,
  recentResolved,
  resolveTvState,
  type FailingResource,
  type TvState,
} from "@/lib/tv-board";

/**
 * TV mode — the wallboard rendering of a status page (spec 2026-08-29-08).
 *
 * The ordinary public page is a document for a reader at 40 cm: sections, rows,
 * charts, a scrollbar. This is the same data for a room at 4 m — one
 * non-scrolling viewport, one dominant signal, and nothing interactive at all.
 *
 * Three rules shape everything below:
 *
 *  1. **The state is never carried by colour alone.** Every ambient tint is
 *     paired with an icon and the state spelled out in words, because roughly
 *     one man in twelve cannot tell this green from this red.
 *  2. **The board never claims to know more than it does.** No availability
 *     number when the page publishes none, no "days since last incident" when
 *     the history is empty, and the whole board drops to grey when the data
 *     stops arriving (see the `stale` prop) — a frozen green screen during an
 *     outage is worse than a dark screen.
 *  3. **Nothing shrinks to fit.** More active incidents than fit are cycled,
 *     not compressed: three unreadable cards convey less than one readable one.
 */

/** How many active incidents are shown at once before the board starts cycling. */
const ACTIVE_INCIDENTS_PER_PAGE = 2;
/**
 * How many failing resources are named at once before the board starts
 * cycling. Higher than the incident allowance because these are one-line rows,
 * not cards — five still read cleanly from across a room.
 */
const FAILING_PER_PAGE = 5;
/** How long each cycle page is held, in ms. */
const CYCLE_MS = 10_000;
/** How many resolved incidents the footer strip recalls. */
const RESOLVED_RECALL = 3;
/** Idle delay before the mouse cursor is hidden, in ms. */
const CURSOR_IDLE_MS = 4_000;
/** How often the ticking durations and the clock are recomputed, in ms. */
const TICK_MS = 1_000;

/** Icon per ambient state — the non-colour half of the signal. */
const STATE_ICON = {
  operational: CheckCircle2,
  degraded: AlertTriangle,
  down: XCircle,
  maintenance: Wrench,
  unknown: HelpCircle,
  stale: WifiOff,
} as const;

/** i18n key for the headline of each ambient state. */
const STATE_HEADLINE: Record<TvState, string> = {
  operational: "allSystemsOperational",
  degraded: "someSystemsDegraded",
  down: "systemOutage",
  maintenance: "underMaintenance",
  unknown: "statusUnknown",
  stale: "tv.stale",
};

/**
 * Maps an ambient state onto the shared status palette.
 *
 * `stale` deliberately borrows the neutral treatment rather than getting a
 * palette of its own: "we do not know" and "we have lost contact" are the same
 * claim to a person looking at the wall, and inventing a sixth colour would
 * only make the five that matter harder to tell apart.
 */
function tvStyle(state: TvState) {
  return statusStyle(state === "stale" ? "unknown" : state);
}

/** A number that only ever ticks whole seconds, for the durations and clock. */
function useNow(intervalMs = TICK_MS): number {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), intervalMs);

    return () => window.clearInterval(timer);
  }, [intervalMs]);

  return now;
}

/** Advances a counter every `intervalMs`, used to page through long lists. */
function useCycleTick(intervalMs: number, enabled: boolean): number {
  const [tick, setTick] = useState(0);

  useEffect(() => {
    if (!enabled) return;

    const timer = window.setInterval(() => setTick((t) => t + 1), intervalMs);

    return () => window.clearInterval(timer);
  }, [intervalMs, enabled]);

  return tick;
}

/**
 * Hides the mouse cursor after a few seconds of stillness.
 *
 * A wallboard is driven by a machine somebody once plugged a mouse into; the
 * arrow then sits frozen in the middle of the screen forever. Any real
 * movement brings it back, so the board stays usable if a person does walk up
 * to it.
 */
function useIdleCursor(): boolean {
  const [idle, setIdle] = useState(false);
  const timerRef = useRef<number | undefined>(undefined);

  useEffect(() => {
    const schedule = () => {
      window.clearTimeout(timerRef.current);
      setIdle(false);
      timerRef.current = window.setTimeout(() => setIdle(true), CURSOR_IDLE_MS);
    };

    schedule();
    window.addEventListener("mousemove", schedule);
    window.addEventListener("touchstart", schedule);

    return () => {
      window.clearTimeout(timerRef.current);
      window.removeEventListener("mousemove", schedule);
      window.removeEventListener("touchstart", schedule);
    };
  }, []);

  return idle;
}

/** Formats a d/h/m duration using the locale's own unit strings. */
function useDurationText(): (ms: number) => string {
  const { t } = useTranslation();

  return (ms: number) => {
    const { days, hours, minutes } = durationParts(ms);

    if (days > 0) return t("tv.durationDaysHours", { days, hours });
    if (hours > 0) return t("tv.durationHoursMinutes", { hours, minutes });

    return t("tv.durationMinutes", { minutes });
  };
}

function ActiveIncidentCard({
  incident,
  now,
  cardClass,
  formatDuration,
}: {
  incident: PublicIncident;
  now: number;
  cardClass: string;
  formatDuration: (ms: number) => string;
}) {
  const { t } = useTranslation();
  const severity = severityStyle(incident.severity);
  const ongoing = elapsedMs(incident.startedAt, now);

  return (
    <article
      data-testid="tv-active-incident"
      data-incident-severity={incident.severity ?? "none"}
      className={`rounded-2xl border px-5 py-4 sm:px-6 sm:py-5 ${cardClass}`}
    >
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h3
          className="min-w-0 break-words text-2xl font-semibold sm:text-3xl"
          data-testid="tv-active-incident-title"
        >
          {incident.title}
        </h3>
        {severity && (
          <span
            className="shrink-0 rounded-full border border-current/30 px-3 py-0.5 text-base font-medium uppercase tracking-wide opacity-90 sm:text-lg"
            data-testid="tv-active-incident-severity"
            translate="no"
          >
            {t(severity.labelKey, { defaultValue: incident.severity })}
          </span>
        )}
      </div>

      {ongoing !== null && (
        <p
          className="mt-1 text-xl opacity-90 sm:text-2xl"
          data-testid="tv-active-incident-duration"
          translate="no"
        >
          {t("tv.ongoingFor", { duration: formatDuration(ongoing) })}
        </p>
      )}

      {incident.affectedResources && incident.affectedResources.length > 0 && (
        <p
          className="mt-1 break-words text-lg opacity-75 sm:text-xl"
          data-testid="tv-active-incident-affected"
        >
          {incident.affectedResources.join(" · ")}
        </p>
      )}
    </article>
  );
}

/**
 * One named failing check.
 *
 * Deliberately a row rather than a card: this panel exists to answer "what
 * broke?" at a glance, and the ambient tint has already answered "how bad?".
 * The status is spelled out in words next to the name, so the row never leans
 * on the surface colour alone to carry meaning.
 */
function FailingResourceRow({
  resource,
  now,
  cardClass,
  formatDuration,
}: {
  resource: FailingResource;
  now: number;
  cardClass: string;
  formatDuration: (ms: number) => string;
}) {
  const { t } = useTranslation();
  const style = statusStyle(resource.status);
  // How long this has been going on. "Checkout API — OUTAGE" says what broke;
  // it does not say whether it broke a minute ago or overnight, which is the
  // first thing anyone walking up to the board asks.
  const ongoing =
    resource.since === undefined ? null : elapsedMs(resource.since, now);

  return (
    <article
      data-testid="tv-failing-resource"
      data-resource-status={resource.status}
      className={`flex items-baseline justify-between gap-4 rounded-2xl border px-5 py-3 sm:px-6 sm:py-4 ${cardClass}`}
    >
      <div className="min-w-0">
        <h3
          className="truncate text-2xl font-semibold sm:text-3xl"
          data-testid="tv-failing-resource-name"
        >
          {resource.name || t("unknown")}
        </h3>
        <p className="truncate text-lg opacity-70 sm:text-xl">
          {ongoing === null
            ? resource.section
            : resource.section
              ? t("tv.failingSectionFor", {
                  section: resource.section,
                  duration: formatDuration(ongoing),
                })
              : t("tv.failingFor", { duration: formatDuration(ongoing) })}
        </p>
      </div>
      <span
        className="shrink-0 rounded-full border border-current/30 px-3 py-0.5 text-base font-medium uppercase tracking-wide opacity-90 sm:text-lg"
        data-testid="tv-failing-resource-status"
        translate="no"
      >
        {t(style.labelKey)}
      </span>
    </article>
  );
}

/**
 * The board.
 *
 * `page` may be undefined while a poll is in flight and the board is already
 * stale — that combination is exactly the outage case, and the board must keep
 * rendering (in grey) rather than unmount into a spinner.
 */
export function TvBoard({
  page,
  incidents,
  stale,
  lastUpdatedAt,
}: {
  page: StatusPage | undefined;
  incidents: PublicIncident[] | undefined;
  stale: boolean;
  lastUpdatedAt: number | undefined;
}) {
  const { t } = useTranslation();
  const now = useNow();
  const cursorIdle = useIdleCursor();
  const formatDuration = useDurationText();

  const active = page?.activeIncidents ?? [];
  const liveState = resolveTvState(page?.overallStatus, active);
  // Staleness OVERRIDES everything, including a green rollup: the last thing a
  // wallboard should do is keep insisting the world is fine with data it can
  // no longer refresh.
  const state: TvState = stale ? "stale" : liveState;
  const style = tvStyle(state);
  const Icon = STATE_ICON[state];

  // Why the board is not green, when the checks alone would not explain it.
  // Not shown while stale: the headline then means "we have lost contact", and
  // attributing that to an incident would be a second, contradictory claim.
  const incidentDriven = stale
    ? 0
    : incidentDrivenCount(page?.overallStatus, active);

  // Only ever computed for a board that is NOT green. A healthy page keeps
  // its "days since last incident" panel untouched — this is a fallback for an
  // unexplained non-green board, not a new permanent fixture.
  const failing = useMemo(
    () => (liveState === "operational" ? [] : failingResources(page)),
    [liveState, page],
  );

  const cycling = active.length > ACTIVE_INCIDENTS_PER_PAGE;
  // Only one of the two middle panels is ever mounted, so both share a single
  // timer rather than racing two of them at the same cadence.
  const failingCycling = active.length === 0 && failing.length > FAILING_PER_PAGE;
  const tick = useCycleTick(CYCLE_MS, cycling || failingCycling);
  const visibleActive = cycleWindow(active, ACTIVE_INCIDENTS_PER_PAGE, tick);
  const visibleFailing = cycleWindow(failing, FAILING_PER_PAGE, tick);

  const history = useMemo(() => incidents ?? [], [incidents]);
  const resolved = useMemo(
    () => recentResolved(history, RESOLVED_RECALL),
    [history],
  );
  const sinceLast = useMemo(() => {
    const last = lastResolvedAt(history);

    return last === null ? null : daysSince(last, now);
    // `now` ticks every second; the day count only changes at midnight, but
    // recomputing a subtraction per second is free and keeps the value honest
    // across a board that has been up for weeks.
  }, [history, now]);

  const availability = page?.overallAvailabilityPct;

  return (
    <div
      data-testid="tv-board"
      data-tv-state={state}
      className={`dark flex h-[100dvh] w-full flex-col overflow-hidden px-6 py-5 sm:px-10 sm:py-8 ${style.tvSurface} ${
        cursorIdle ? "cursor-none" : ""
      }`}
    >
      {/* --- Ambient state: icon + word + colour, never colour alone. --- */}
      <header className="flex min-w-0 shrink-0 items-center gap-4 sm:gap-6">
        <Icon
          className={`h-14 w-14 shrink-0 sm:h-20 sm:w-20 ${style.tvAccent}`}
          aria-hidden="true"
          data-testid="tv-state-icon"
        />
        <div className="min-w-0 flex-1">
          <h1
            className={`truncate text-4xl font-bold leading-tight sm:text-6xl ${style.tvAccent}`}
            data-testid="tv-headline"
          >
            {t(STATE_HEADLINE[state])}
          </h1>
          <p
            className="truncate text-xl opacity-75 sm:text-2xl"
            data-testid="tv-page-name"
          >
            {page?.name ?? t("solidpingStatus")}
          </p>
          {incidentDriven > 0 && (
            <p
              className="truncate text-lg opacity-70 sm:text-xl"
              data-testid="tv-headline-cause"
            >
              {t("tv.incidentDriven", { count: incidentDriven })}
            </p>
          )}
        </div>

        {/* --- The one big number, only when the page publishes one. --- */}
        {availability !== undefined && (
          <div className="shrink-0 text-right" data-testid="tv-availability">
            <div
              className={`text-5xl font-bold tabular-nums sm:text-7xl ${style.tvAccent}`}
              translate="no"
            >
              {availability.toFixed(2)}%
            </div>
            <div className="text-base uppercase tracking-wide opacity-70 sm:text-lg">
              {t(`tv.uptimeWindow.${page?.historyPeriod ?? "90d"}`, {
                defaultValue: t("uptime"),
              })}
            </div>
          </div>
        )}
      </header>

      {/* --- Middle: whatever is most worth the room's attention. --- */}
      <main className="mt-6 flex min-h-0 flex-1 flex-col justify-center gap-4">
        {stale ? (
          <p
            className="text-2xl opacity-80 sm:text-3xl"
            data-testid="tv-stale-notice"
            translate="no"
          >
            {t("tv.staleSince", {
              time:
                lastUpdatedAt === undefined
                  ? "—"
                  : new Date(lastUpdatedAt).toLocaleTimeString(),
            })}
          </p>
        ) : visibleActive.length > 0 ? (
          <div className="space-y-4" data-testid="tv-active-incidents">
            {visibleActive.map((incident) => (
              <ActiveIncidentCard
                key={incident.uid}
                incident={incident}
                now={now}
                cardClass={style.tvCard}
                formatDuration={formatDuration}
              />
            ))}
            {cycling && (
              <p className="text-lg opacity-60" data-testid="tv-cycling-note">
                {t("tv.cycling", { count: active.length })}
              </p>
            )}
          </div>
        ) : visibleFailing.length > 0 ? (
          /* The board is not green but nothing is published yet — which is the
             normal state for the first `autoPublishDelaySeconds` of every
             outage, and the permanent state when auto-publish is off. Name the
             checks that are actually failing instead of leaving the room to
             stare at an unexplained red screen. */
          <div className="space-y-3" data-testid="tv-failing-resources">
            <h2 className="text-xl uppercase tracking-wide opacity-70 sm:text-2xl">
              {t("tv.affectedTitle")}
            </h2>
            {visibleFailing.map((resource) => (
              <FailingResourceRow
                key={resource.uid}
                resource={resource}
                now={now}
                cardClass={style.tvCard}
                formatDuration={formatDuration}
              />
            ))}
            {failingCycling && (
              <p
                className="text-lg opacity-60"
                data-testid="tv-failing-cycling"
              >
                {t("tv.affectedCycling", { count: failing.length })}
              </p>
            )}
          </div>
        ) : (
          /* No incident is open: the room gets the reassuring number instead.
             Hidden while an incident IS open — "42 days since the last
             incident" next to a live outage is a contradiction on a wall. */
          <div data-testid="tv-days-since">
            {sinceLast === null ? (
              <p className="text-3xl opacity-75 sm:text-4xl">
                {t("tv.noIncidentsRecorded")}
              </p>
            ) : (
              <p className="text-3xl opacity-90 sm:text-5xl" translate="no">
                {t("tv.daysSinceLastIncident", { count: sinceLast })}
              </p>
            )}
          </div>
        )}
      </main>

      {/* --- Recently resolved: the room's short-term memory. --- */}
      {resolved.length > 0 && !stale && (
        <section
          className="mt-4 shrink-0 grid gap-3 sm:grid-cols-3"
          aria-label={t("incidentHistory")}
          data-testid="tv-resolved"
        >
          {resolved.map((incident) => {
            const took =
              incident.resolvedAt === undefined
                ? null
                : elapsedMs(incident.startedAt, incident.resolvedAt);
            // How long ago it STARTED. "resolved in 8m" on its own tells the
            // room how bad it was but not whether it happened over lunch or
            // last Tuesday — and a wallboard is read by someone who was not
            // watching when it happened. Start + duration pins the whole event
            // down, and the end is the sum of the two.
            const ago = elapsedMs(incident.startedAt, now);

            return (
              <article
                key={incident.uid}
                className={`min-w-0 rounded-xl border px-4 py-3 ${style.tvCard}`}
                data-testid="tv-resolved-incident"
              >
                <h4 className="truncate text-lg font-medium sm:text-xl">
                  {incident.title}
                </h4>
                <p className="text-base opacity-70 sm:text-lg" translate="no">
                  {ago === null
                    ? ""
                    : took === null
                      ? t("tv.resolvedAgo", { ago: formatDuration(ago) })
                      : t("tv.resolvedAgoFor", {
                          ago: formatDuration(ago),
                          duration: formatDuration(took),
                        })}
                </p>
              </article>
            );
          })}
        </section>
      )}

      {/* --- Footer: whose board this is, and proof it is alive. --- */}
      <footer className="mt-5 flex shrink-0 items-center justify-between gap-4 text-lg opacity-70 sm:text-xl">
        <span className="truncate" data-testid="tv-footer-brand">
          {page?.name ?? t("solidpingStatus")}
        </span>
        <span className="flex shrink-0 items-center gap-2" translate="no">
          <Activity
            className={`h-5 w-5 ${stale ? "" : "animate-pulse"} ${style.tvAccent}`}
            aria-hidden="true"
            data-testid="tv-pulse"
          />
          <span data-testid="tv-updated-at">
            {t("tv.updatedAt", {
              time:
                lastUpdatedAt === undefined
                  ? "—"
                  : new Date(lastUpdatedAt).toLocaleTimeString(),
            })}
          </span>
        </span>
      </footer>
    </div>
  );
}
