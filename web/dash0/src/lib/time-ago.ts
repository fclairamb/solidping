// Pure formatting helpers for the <TimeAgo> component
// (@/components/ui/time-ago). Kept here, framework-free, so the tiering and
// ISO formatting can be unit-tested directly instead of only through a
// rendered component.

function pad(n: number): string {
  return n.toString().padStart(2, "0");
}

/**
 * Compact relative string: "just now" / "Nm ago" / "Nh ago" / "Nd ago",
 * falling back to a locale date past 30 days. This is the canonical
 * relative-time tiering for the app (carried over verbatim from the old
 * incidents-list `formatRelativeTime` helper) — every <TimeAgo> call site
 * (incidents list, incident detail, jobs) now renders identically instead of
 * each page inventing its own wording (some previously used date-fns'
 * `formatDistanceToNow`, which is far more verbose: "about 2 hours ago").
 */
export function formatRelativeTime(date: Date, now: Date = new Date()): string {
  const diffMs = now.getTime() - date.getTime();
  const diffSec = Math.floor(diffMs / 1000);
  const diffMin = Math.floor(diffSec / 60);
  const diffHour = Math.floor(diffMin / 60);
  const diffDay = Math.floor(diffHour / 24);

  if (diffDay > 30) return date.toLocaleDateString();
  if (diffDay > 0) return `${diffDay}d ago`;
  if (diffHour > 0) return `${diffHour}h ago`;
  if (diffMin > 0) return `${diffMin}m ago`;
  return "just now";
}

/**
 * "2026-08-14" in the browser's local timezone, ISO-ordered (year-month-day)
 * so it reads unambiguously regardless of the user's locale — unlike
 * `Date.toLocaleDateString()`, which flips to MM/DD/YYYY for en-US and
 * DD/MM/YYYY for most everyone else. Shared by `formatLocalDateTime` and the
 * inline-variant date prefix (`formatInlineAbsolute`) so both stay in sync.
 */
function formatLocalDate(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

/**
 * "2026-08-14 11:31:07" in the browser's local timezone, ISO-ordered
 * (year-month-day) so it reads unambiguously regardless of the user's
 * locale — unlike `Date.toLocaleString()`, which flips to MM/DD/YYYY for
 * en-US and DD/MM/YYYY for most everyone else.
 */
export function formatLocalDateTime(date: Date): string {
  return `${formatLocalDate(date)} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

/**
 * ISO 8601 UTC without milliseconds: "2026-08-14T09:31:07Z". This is the
 * click-to-copy format — unambiguous, sortable, and paste-able straight into
 * a log query or an external status page's search box.
 */
export function formatUtcIso(date: Date): string {
  return date.toISOString().replace(/\.\d{3}Z$/, "Z");
}

/**
 * Short local-timezone marker for the inline clock below: the runtime's own
 * abbreviation ("CEST", "EST", …) via `Intl`, falling back to a UTC-offset
 * label ("UTC+2") when `Intl` can't produce one (unsupported runtime, or a
 * `timeZoneName` part missing from the formatted output). The inline clock
 * must never render with no marker at all — that would read as UTC by
 * default, which is exactly the ambiguity this function exists to remove.
 */
function getLocalZoneMarker(date: Date): string {
  try {
    const parts = new Intl.DateTimeFormat(undefined, { timeZoneName: "short" }).formatToParts(
      date,
    );
    const zoneName = parts.find((p) => p.type === "timeZoneName")?.value;
    if (zoneName) return zoneName;
  } catch {
    // Fall through to the offset label below.
  }
  const offsetMin = -date.getTimezoneOffset();
  const sign = offsetMin >= 0 ? "+" : "-";
  const abs = Math.abs(offsetMin);
  const hours = Math.floor(abs / 60);
  const mins = abs % 60;
  return mins === 0 ? `UTC${sign}${hours}` : `UTC${sign}${hours}:${pad(mins)}`;
}

/**
 * "11:31:07 CEST", prefixed with an ISO-ordered local date when `date` isn't
 * the same calendar day as `now` — the inline-variant absolute display used
 * on the incident detail page, where several timestamps are compared at
 * once. Renders in the browser's LOCAL time (not UTC): the primary reader is
 * an operator comparing the incident against their own wall clock: UTC is
 * still available, one step away, in the hover tooltip
 * (`formatTooltipText`) and the click-to-copy payload (`formatUtcIso`).
 */
export function formatInlineAbsolute(date: Date, now: Date = new Date()): string {
  // The rendered clock below is LOCAL, so the "today" check must also
  // compare LOCAL calendar days — toDateString() does exactly that (it never
  // looks at UTC). Keep this pair on the same timezone; a mismatch here is
  // what let a UTC-clock/local-day pairing print a bare, dateless time that
  // actually belonged to a different calendar day (see spec Problem #1).
  const isToday = date.toDateString() === now.toDateString();
  const time = `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())} ${getLocalZoneMarker(date)}`;
  return isToday ? time : `${formatLocalDate(date)} ${time}`;
}

/**
 * "2026-08-14 11:31:07 (local) · 2026-08-14T09:31:07Z" — the tooltip body
 * shown on hover/tap for both <TimeAgo> variants: local time first (what the
 * operator's wall clock reads), then unambiguous UTC.
 */
export function formatTooltipText(date: Date): string {
  return `${formatLocalDateTime(date)} (local) · ${formatUtcIso(date)}`;
}
