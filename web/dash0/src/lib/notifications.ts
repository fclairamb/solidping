import type { IncidentNotification } from "@/api/hooks";

/** Maps a notification delivery status to the Badge variant used to render it.
 * Shared by the incident notifications table and the notification detail page. */
export function notificationStatusVariant(
  status: IncidentNotification["status"],
): "default" | "secondary" | "destructive" | "outline" {
  switch (status) {
    case "sent":
      return "default";
    case "failed":
      return "destructive";
    case "pending":
      return "outline";
    case "skipped":
    case "cancelled":
    default:
      return "secondary";
  }
}

/** Human-readable label for a notification source, including the escalation
 * cycle when the row was produced by a repeated escalation. */
export function sourceLabel(source: string, repeatIndex?: number): string {
  const cycle =
    repeatIndex !== undefined && repeatIndex > 0 ? ` (cycle ${repeatIndex + 1})` : "";
  switch (source) {
    case "check_connection":
      return "Check connection";
    case "escalation_user":
      return `Escalation step${cycle}`;
    case "escalation_schedule":
      return `On-call schedule${cycle}`;
    case "escalation_all_admins":
      return `All admins${cycle}`;
    case "escalation_connection":
      return `Escalation connection${cycle}`;
    default:
      return source;
  }
}
