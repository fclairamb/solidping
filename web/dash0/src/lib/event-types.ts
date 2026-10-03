// ALL_EVENT_TYPES is the full catalogue of audit event types the server can
// write, ordered the way the events page's type filter offers them: grouped
// by family, with the families ordered the way an operator reads a trail —
// what happened to the service, then the platform's own notices, then who got
// in, then what they changed.
//
// The authoritative list lives in `EventType*` constants in
// server/internal/db/models/event.go. event-types.test.ts parses that file and
// fails when the two drift, so a new event type cannot ship without landing
// here too — and neither can one be dropped from here while the server still
// writes it.

/** Event families, in the order the type filter groups them. */
export const EVENT_TYPE_FAMILIES = [
  "check",
  "incident",
  "statuspage",
  "status_update",
  "region",
  "agent",
  "org",
  "auth",
  "member",
  "integration",
  "escalation_policy",
  "oncall_schedule",
  "status_page",
  "maintenance_window",
  "config",
] as const;

export type EventTypeFamily = (typeof EVENT_TYPE_FAMILIES)[number];

/** Every event type, grouped by family in EVENT_TYPE_FAMILIES order. */
export const ALL_EVENT_TYPES = [
  "check.created",
  "check.updated",
  "check.deleted",
  "check.placement_changed",
  "check.baseline_captured",
  "check.ai_repair_attempted",
  "check.ai_repair_proposed",
  "check.ai_repair_applied",
  "check.ai_usage",

  "incident.created",
  "incident.escalated",
  "incident.escalation_failed",
  "incident.resolved",
  "incident.reopened",
  "incident.rolled_up",
  "incident.rollup_detached",
  "incident.monitoring_interrupted",
  "incident.monitoring_resumed",
  "incident.components_changed",
  "incident.acknowledged",
  "incident.unacknowledged",
  "incident.snoozed",
  "incident.unsnoozed",
  "incident.comment",

  "statuspage.incident.published",
  "statuspage.incident.updated",
  "statuspage.incident.resolved",
  "statuspage.subscriber.disabled",
  "statuspage.custom_domain.demoted",

  "status_update.created",
  "status_update.updated",
  "status_update.deleted",

  "region.offline",
  "region.recovered",

  "agent.connected",
  "agent.disconnected",

  "org.activation.signup_completed",
  "org.activation.first_check_created",
  "org.activation.first_result_received",
  "org.activation.first_notification_configured",
  "org.activation.first_incident_paged",
  "org.settings_updated",

  "auth.login_succeeded",
  "auth.login_failed",
  "auth.logout",
  "auth.token_created",
  "auth.token_revoked",
  "auth.token_misuse",
  "auth.impersonation_started",
  "auth.email_changed",

  "member.invited",
  "member.joined",
  "member.removed",
  "member.role_changed",

  "integration.created",
  "integration.updated",
  "integration.deleted",

  "escalation_policy.created",
  "escalation_policy.updated",
  "escalation_policy.deleted",

  "oncall_schedule.created",
  "oncall_schedule.updated",
  "oncall_schedule.deleted",

  "status_page.created",
  "status_page.updated",
  "status_page.deleted",

  "maintenance_window.created",
  "maintenance_window.updated",
  "maintenance_window.deleted",

  "config.applied",
] as const;

export type EventType = (typeof ALL_EVENT_TYPES)[number];

/** Narrow an arbitrary value — a URL search param, an API payload — to an
 * entry of the catalogue, so a hand-edited `?type=anything` can be rejected
 * instead of being sent to the server as a filter that cannot match. */
export function isEventType(value: unknown): value is EventType {
  return (
    typeof value === "string" &&
    (ALL_EVENT_TYPES as readonly string[]).includes(value)
  );
}

/** The family a type belongs to: everything before its first dot.
 *
 * Deliberately prefix-based rather than a lookup table — the API's `type=`
 * filter matches on exactly this prefix, so what groups the UI and what
 * narrows the query can never disagree. */
export function eventFamily(eventType: string): string {
  const dot = eventType.indexOf(".");
  return dot === -1 ? eventType : eventType.slice(0, dot);
}

/** The types of one family, in catalogue order. */
export function eventTypesOfFamily(family: string): EventType[] {
  return ALL_EVENT_TYPES.filter((type) => eventFamily(type) === family);
}
