import type { MaintenanceOccurrence, MaintenanceWindow } from "@/api/hooks";
import { occurrenceStartUTC } from "@/lib/maintenance-window-status";

// Client-side rendering helpers for maintenance-window schedules:
//  - nextOccurrences: a UTC-anchored port of the backend models.NextOccurrences,
//    used as a fallback when the server response omits nextOccurrences and to
//    drive the live form preview (which must reflect unsaved edits).
//  - describeSchedule: a localized one-line human summary of a window.
//  - formatDuration: a compact "1h" / "30m" / "2h 30m" / "1d" string.
//
// All dates/times for the summary are rendered with Intl (the browser's locale
// and timezone). Occurrence stepping itself is done in UTC to match the server.

// i18n translator shape we depend on (a subset of react-i18next's TFunction).
type Translate = (key: string, opts?: Record<string, unknown>) => string;

const MAX_NEXT_OCCURRENCES = 100;

// nextOccurrences returns up to n occurrences whose end is at/after `from`, in
// chronological order, honoring recurrenceEnd. The currently-active occurrence
// (if any) is included first. For recurrence "none" it returns the single
// window when its end is still after `from`, else an empty list.
//
// This mirrors models.NextOccurrences: every occurrence is anchored on the
// original startAt via occurrenceStartUTC(start, recurrence, k), so the
// monthly month-end clamp does not accumulate drift.
export function nextOccurrences(
  w: MaintenanceWindow,
  from: Date,
  n: number,
): MaintenanceOccurrence[] {
  if (n <= 0) return [];
  const count = Math.min(n, MAX_NEXT_OCCURRENCES);

  const start = new Date(w.startAt);
  const end = new Date(w.endAt);
  const durationMs = end.getTime() - start.getTime();
  const recEnd = w.recurrenceEnd ? new Date(w.recurrenceEnd) : null;

  if (w.recurrence === "none") {
    if (end.getTime() <= from.getTime()) return [];
    return [{ startAt: w.startAt, endAt: w.endAt }];
  }

  // Estimate the first occurrence index that could still be active at `from`,
  // then start a couple of steps earlier so the active occurrence isn't skipped.
  const stepMs =
    occurrenceStartUTC(start, w.recurrence, 1).getTime() - start.getTime();
  let startK = 0;
  if (from.getTime() > start.getTime() && stepMs > 0) {
    startK = Math.floor((from.getTime() - start.getTime()) / stepMs) - 2;
    if (startK < 0) startK = 0;
  }

  const out: MaintenanceOccurrence[] = [];
  for (
    let k = startK;
    out.length < count && k < startK + MAX_NEXT_OCCURRENCES;
    k++
  ) {
    const occStart = occurrenceStartUTC(start, w.recurrence, k);
    const occEndMs = occStart.getTime() + durationMs;

    if (recEnd && occStart.getTime() > recEnd.getTime()) break;
    // Skip occurrences entirely in the past relative to `from`.
    if (occEndMs <= from.getTime()) continue;

    out.push({
      startAt: occStart.toISOString(),
      endAt: new Date(occEndMs).toISOString(),
    });
  }

  return out;
}

// formatDuration renders a millisecond duration as a compact human string using
// the duration.*Short i18n unit keys: "1d", "2h", "30m", "2h 30m", "1d 3h".
export function formatDuration(ms: number, t: Translate): string {
  if (!Number.isFinite(ms) || ms <= 0) return "";
  const totalMinutes = Math.round(ms / 60000);
  const days = Math.floor(totalMinutes / (60 * 24));
  const hours = Math.floor((totalMinutes % (60 * 24)) / 60);
  const minutes = totalMinutes % 60;

  const d = t("duration.daysShort");
  const h = t("duration.hoursShort");
  const m = t("duration.minutesShort");

  const parts: string[] = [];
  if (days > 0) parts.push(`${days}${d}`);
  if (hours > 0) parts.push(`${hours}${h}`);
  // Show minutes when there are leftover minutes, or when nothing else showed.
  if (minutes > 0 || parts.length === 0) parts.push(`${minutes}${m}`);
  return parts.join(" ");
}

// Format a wall-clock time (HH:MM) of an instant in the browser's locale.
function fmtTime(d: Date): string {
  return d.toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
  });
}

// Format a date (no time) in the browser's locale.
function fmtDate(d: Date): string {
  return d.toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

// Format a full date+time in the browser's locale.
function fmtDateTime(d: Date): string {
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// describeSchedule returns a localized one-line human summary of the window,
// e.g. "Every Wednesday, 22:00 – 23:00 (1h) · no end date". Weekday/date/time
// names come from Intl; the day-of-month is phrased as "day {{day}}" to avoid
// locale ordinals.
export function describeSchedule(w: MaintenanceWindow, t: Translate): string {
  const start = new Date(w.startAt);
  const end = new Date(w.endAt);
  if (Number.isNaN(start.getTime()) || Number.isNaN(end.getTime())) {
    return "";
  }

  const startTime = fmtTime(start);
  const endTime = fmtTime(end);
  const duration = formatDuration(end.getTime() - start.getTime(), t);

  if (w.recurrence === "none") {
    return t("summary.once", {
      start: fmtDateTime(start),
      end: endTime,
    });
  }

  let base: string;
  if (w.recurrence === "daily") {
    base = t("summary.daily", { start: startTime, end: endTime, duration });
  } else if (w.recurrence === "weekly") {
    const weekday = start.toLocaleDateString(undefined, { weekday: "long" });
    base = t("summary.weekly", {
      weekday,
      start: startTime,
      end: endTime,
      duration,
    });
  } else {
    // monthly: anchor day-of-month (capped display at 28 in the form, but the
    // summary reflects whatever the window actually stores).
    const day = start.getUTCDate();
    base = t("summary.monthly", {
      day,
      start: startTime,
      end: endTime,
      duration,
    });
  }

  const tail = w.recurrenceEnd
    ? t("summary.until", { date: fmtDate(new Date(w.recurrenceEnd)) })
    : t("summary.noEnd");

  return `${base}${tail}`;
}

// Reusable Intl date renderer for a single occurrence (used by summary/detail
// "Next:" lines). Exported so callers don't re-implement formatting.
export function formatOccurrenceDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return fmtDateTime(d);
}
