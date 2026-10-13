// Incident reminders (spec 2026-10-12-16): an open, unacknowledged incident
// re-notifies the check's channels every interval. A check's value is null
// (inherit the org default), 0 (off) or 900..86400 seconds.

/** Select value meaning "inherit the organization default". */
export const REMINDER_INHERIT = "inherit";

/** The intervals the dashboard offers, in seconds. 0 is off. */
export const REMINDER_INTERVAL_PRESETS = [0, 900, 1800, 3600, 7200, 14400, 86400] as const;

export const REMINDER_INTERVAL_MIN_SECONDS = 900;
export const REMINDER_INTERVAL_MAX_SECONDS = 86400;

/** Whether a value is storable: 0 (off) or 15 minutes to 24 hours. */
export function isValidReminderInterval(seconds: number): boolean {
  return (
    seconds === 0 ||
    (Number.isInteger(seconds) &&
      seconds >= REMINDER_INTERVAL_MIN_SECONDS &&
      seconds <= REMINDER_INTERVAL_MAX_SECONDS)
  );
}

/**
 * A compact duration for a reminder interval: "15 min", "1 h", "1 h 30 min".
 * Returns null for 0, which the caller renders as its translated "Off".
 */
export function reminderDurationLabel(seconds: number): string | null {
  if (seconds <= 0) return null;

  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes} min`;

  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;

  return rest === 0 ? `${hours} h` : `${hours} h ${rest} min`;
}

/** The select value for a stored check value (null means inherit). */
export function reminderSelectValue(seconds: number | null | undefined): string {
  return seconds == null ? REMINDER_INHERIT : String(seconds);
}

/** The request value for a select value: null for inherit, else the seconds. */
export function reminderRequestValue(selectValue: string): number | null {
  if (selectValue === REMINDER_INHERIT) return null;

  const seconds = Number.parseInt(selectValue, 10);

  return Number.isNaN(seconds) ? null : seconds;
}

/**
 * The options a select offers: the presets, plus the stored value when it is a
 * valid interval the presets do not list (set through the API), so editing a
 * check never silently changes it.
 */
export function reminderOptions(current: number | null | undefined): number[] {
  const options: number[] = [...REMINDER_INTERVAL_PRESETS];

  if (current != null && !options.includes(current) && isValidReminderInterval(current)) {
    options.push(current);
    options.sort((a, b) => a - b);
  }

  return options;
}
