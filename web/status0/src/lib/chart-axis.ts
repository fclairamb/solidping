// Numeric time-axis math for the response-time chart (spec 2026-09-21-03 B).
//
// Recharts used to be handed `time` strings on a category axis: every row got
// an equal slice of the plot width regardless of its timestamp, so a 39-day
// gap and a 20-second gap drew identically, and the tick picker sampled rows
// instead of instants (an axis could read "21 sept. · 11:44 · 11:49"). The
// chart now carries an epoch-ms field and renders
// `type="number" scale="time" domain={[min, max]}` — the same shape dash0's
// response-time chart uses — so spacing follows time and ticks are picked
// over the TIME domain.

/** The axis geometry the chart feeds straight to recharts. */
export interface TimeAxis {
  domain: [number, number];
  ticks: number[];
}

// Recharts puts one tick per category by default, so the tick count used to
// follow the sample count. A small, evenly spaced set picked over the time
// domain replaces that.
const MAX_TICKS = 5;

/**
 * Picks evenly spaced INSTANTS over the time domain covered by `times` — not
 * evenly spaced rows — so the axis can never compress 39 days and 16 minutes
 * into the same visual distance. `times` may be unsorted and may hold
 * unparseable (NaN) entries; both are ignored.
 */
export function computeTimeAxis(
  times: readonly number[],
  maxTicks = MAX_TICKS,
): TimeAxis {
  let min = Infinity;
  let max = -Infinity;

  for (const ms of times) {
    if (!Number.isFinite(ms)) continue;
    if (ms < min) min = ms;
    if (ms > max) max = ms;
  }

  if (!Number.isFinite(min) || !Number.isFinite(max)) {
    return { domain: [0, 0], ticks: [] };
  }

  if (min === max) return { domain: [min, max], ticks: [min] };

  const span = max - min;
  const ticks: number[] = [];
  for (let i = 0; i < maxTicks; i++) {
    ticks.push(Math.round(min + (span * i) / (maxTicks - 1)));
  }

  return { domain: [min, max], ticks };
}

const ONE_HOUR = 3_600_000;
const ONE_DAY = 24 * ONE_HOUR;

/**
 * Labels a tick at a granularity derived from the DOMAIN span (dash0's
 * adaptive tiers): sub-hour shows seconds, sub-day shows the clock, a few
 * days show weekday + clock (so two ticks 12 hours apart never read the
 * same), and a multi-day span prints the date — which is what kills the old
 * "21 sept. · 11:44 · 11:49" axis, whose day labels repeated while the
 * clock jumped.
 */
export function formatAxisTick(ms: number, spanMs: number, locale: string): string {
  const date = new Date(ms);

  if (spanMs < ONE_HOUR) {
    return date.toLocaleTimeString(locale, {
      hour: "numeric",
      minute: "2-digit",
      second: "2-digit",
    });
  }

  if (spanMs < ONE_DAY) {
    return date.toLocaleTimeString(locale, {
      hour: "numeric",
      minute: "2-digit",
    });
  }

  if (spanMs < 7 * ONE_DAY) {
    return `${date.toLocaleDateString(locale, { weekday: "short" })} ${date.toLocaleTimeString(locale, { hour: "numeric", minute: "2-digit" })}`;
  }

  return date.toLocaleDateString(locale, { month: "short", day: "numeric" });
}