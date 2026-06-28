import type { MaintenanceWindow } from "@/api/hooks";

// Client-side port of server's models.IsActiveAt / models.Status. The
// maintenance-window API now carries a server-computed `status`, but the
// dashboard still computes it locally as a fallback (older servers) and to
// reflect unsaved edits in the form preview.
//
// Recurrence is anchored on the ORIGINAL startAt: occurrence k is
// occurrenceStartUTC(startAt, recurrence, k). Monthly clamps the day-of-month
// to the target month's length, so the month-end clamp never accumulates drift
// across occurrences (e.g. Jan 31 -> Feb 28 -> Mar 31, not Mar 3). All stepping
// is done in UTC to match the backend, which steps with time.AddDate in UTC.

export type MaintenanceStatus = "active" | "upcoming" | "past";

// Number of days in the given (UTC) year/month. month is 0-based.
function daysInMonthUTC(year: number, month: number): number {
  // Day 0 of the next month is the last day of this month.
  return new Date(Date.UTC(year, month + 1, 0)).getUTCDate();
}

// occurrenceStartUTC returns the start of the k-th occurrence of a recurring
// window, anchored on `start`. Mirrors the backend adders (addDays/addWeeks/
// addMonths) operating in UTC.
export function occurrenceStartUTC(
  start: Date,
  recurrence: string,
  k: number,
): Date {
  if (recurrence === "daily") {
    return new Date(Date.UTC(
      start.getUTCFullYear(),
      start.getUTCMonth(),
      start.getUTCDate() + k,
      start.getUTCHours(),
      start.getUTCMinutes(),
      start.getUTCSeconds(),
      start.getUTCMilliseconds(),
    ));
  }
  if (recurrence === "weekly") {
    return new Date(Date.UTC(
      start.getUTCFullYear(),
      start.getUTCMonth(),
      start.getUTCDate() + k * 7,
      start.getUTCHours(),
      start.getUTCMinutes(),
      start.getUTCSeconds(),
      start.getUTCMilliseconds(),
    ));
  }
  // monthly: shift the month off the first-of-month (never overflows), then
  // clamp the original day-of-month to the target month's length.
  const targetYear = start.getUTCFullYear();
  const targetMonthIndex = start.getUTCMonth() + k;
  // Normalize to a concrete year/month using a first-of-month date.
  const first = new Date(Date.UTC(targetYear, targetMonthIndex, 1));
  const y = first.getUTCFullYear();
  const m = first.getUTCMonth();
  const day = Math.min(start.getUTCDate(), daysInMonthUTC(y, m));
  return new Date(Date.UTC(
    y,
    m,
    day,
    start.getUTCHours(),
    start.getUTCMinutes(),
    start.getUTCSeconds(),
    start.getUTCMilliseconds(),
  ));
}

// Estimate of the occurrence index k such that occurrenceStartUTC(start, k) is
// at or just before target. May over/undershoot by a step near month
// boundaries, so callers scan a small window around it.
function occurrenceIndex(start: Date, recurrence: string, target: Date): number {
  if (target.getTime() <= start.getTime()) return 0;
  const stepMs =
    occurrenceStartUTC(start, recurrence, 1).getTime() - start.getTime();
  if (stepMs <= 0) return 0;
  const k = Math.floor((target.getTime() - start.getTime()) / stepMs);
  return k < 0 ? 0 : k;
}

export function isActiveAt(w: MaintenanceWindow, t: Date): boolean {
  const start = new Date(w.startAt);
  const end = new Date(w.endAt);
  if (w.recurrence === "none") return t >= start && t < end;
  if (w.recurrenceEnd && t > new Date(w.recurrenceEnd)) return false;
  if (t < start) return false;

  const durationMs = end.getTime() - start.getTime();
  const k = occurrenceIndex(start, w.recurrence, t);

  // Scan a small window around the estimate to absorb month-length variance,
  // finding the latest occurrence whose start <= target.
  let best = -1;
  for (let i = k - 2; i <= k + 2; i++) {
    if (i < 0) continue;
    const occStart = occurrenceStartUTC(start, w.recurrence, i);
    if (occStart.getTime() <= t.getTime()) best = i;
  }
  if (best < 0) return false;

  const occStart = occurrenceStartUTC(start, w.recurrence, best);
  return (
    t.getTime() >= occStart.getTime() &&
    t.getTime() < occStart.getTime() + durationMs
  );
}

export function computeMaintenanceStatus(
  w: MaintenanceWindow,
  now = new Date(),
): MaintenanceStatus {
  if (isActiveAt(w, now)) return "active";
  if (w.recurrence === "none") {
    return now < new Date(w.startAt) ? "upcoming" : "past";
  }
  if (w.recurrenceEnd && now > new Date(w.recurrenceEnd)) return "past";
  return "upcoming";
}

// Badge variant for each status (matches the variants shipped on the Badge
// primitive — see design-reference.tsx).
export function maintenanceStatusBadgeVariant(
  status: MaintenanceStatus,
): "success" | "secondary" | "outline" {
  switch (status) {
    case "active":
      return "success";
    case "upcoming":
      return "secondary";
    case "past":
      return "outline";
  }
}
