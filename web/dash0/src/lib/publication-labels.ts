import type { TFunction } from "i18next";

/**
 * Human-readable labels for the two enums an incident publication carries: its
 * lifecycle state and its severity.
 *
 * Both used to be rendered raw — `{publication.state}` printed "investigating"
 * into a badge, in English, on an otherwise French page — which is the same
 * defect the surrounding components were translated to fix.
 *
 * The state labels are REUSED from `statusUpdates:kinds.*` rather than
 * duplicated into `incidents.json`. A publication's state and a status
 * update's kind are the same lifecycle vocabulary on the same feature, and a
 * second copy of four words in four locales is a drift waiting to happen: the
 * picker and the badge would eventually disagree. Keys are fully qualified, so
 * the caller's `t` may come from any namespace.
 *
 * The fallbacks are the point of the explicit allow-lists. `t()` returns the
 * key itself on a miss, so a state the backend adds before this frontend
 * catches up would otherwise leak `statusUpdates:kinds.degraded` into a badge.
 * A humanized form of the raw value is wrong-but-readable; a raw i18n key is
 * neither.
 */

/**
 * Publication states and status-update kinds that have a translated label.
 *
 * `PublicationState` (four values) is a subset of the update kinds (six) —
 * both are covered here because the publication timeline badges an update's
 * `kind` from the same vocabulary, and `kind` is typed as a plain string.
 */
const STATE_KEYS: Record<string, string> = {
  investigating: "statusUpdates:kinds.investigating",
  identified: "statusUpdates:kinds.identified",
  monitoring: "statusUpdates:kinds.monitoring",
  resolved: "statusUpdates:kinds.resolved",
  maintenance: "statusUpdates:kinds.maintenance",
  info: "statusUpdates:kinds.info",
};

const SEVERITY_KEYS: Record<string, string> = {
  minor: "incidents:publications.severityMinor",
  major: "incidents:publications.severityMajor",
  critical: "incidents:publications.severityCritical",
};

// humanize turns an unrecognized raw value like "partial_outage" into
// "Partial Outage" rather than showing a raw i18n key or snake_case.
function humanize(raw: string): string {
  return raw
    .split(/[_-]+/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");
}

/**
 * Returns the badge text for a publication's state, or for a publication
 * update's kind — they share one vocabulary.
 */
export function publicationStateLabel(
  t: TFunction,
  state: string | undefined | null,
): string {
  if (!state) return "";

  const key = STATE_KEYS[state];
  return key ? t(key) : humanize(state);
}

/** Returns the label for a publication severity (`minor` / `major` / `critical`). */
export function publicationSeverityLabel(
  t: TFunction,
  severity: string | undefined | null,
): string {
  if (!severity) return "";

  const key = SEVERITY_KEYS[severity];
  return key ? t(key) : humanize(severity);
}
