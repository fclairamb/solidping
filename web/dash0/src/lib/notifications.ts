import type { TFunction } from "i18next";

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

/**
 * Human-readable label for a notification source, including the escalation
 * cycle when the row was produced by a repeated escalation.
 *
 * These are the VALUES in the delivery history's Source column, whose headers
 * an fr/de/es operator already reads in their own language — so the values
 * have to follow. `t` comes from the `common` namespace and is passed in, the
 * same way `channelTypeLabel` in ./channel-labels takes it: it keeps this a
 * pure function and re-renders on a language change.
 *
 * An unrecognised source falls through to the raw token rather than an i18n
 * key: the backend can add one before this frontend catches up, and a raw
 * token is wrong-but-readable where `common.notificationSources.foo` is not.
 */
export function sourceLabel(
  t: TFunction,
  source: string,
  repeatIndex?: number,
): string {
  const cycle =
    repeatIndex !== undefined && repeatIndex > 0
      ? ` (${t("notificationSources.cycle", { cycle: repeatIndex + 1 })})`
      : "";
  switch (source) {
    case "check_connection":
      return t("notificationSources.checkConnection");
    case "escalation_user":
      return `${t("notificationSources.escalationStep")}${cycle}`;
    case "escalation_schedule":
      return `${t("notificationSources.onCallSchedule")}${cycle}`;
    case "escalation_all_admins":
      return `${t("notificationSources.allAdmins")}${cycle}`;
    case "escalation_connection":
      return `${t("notificationSources.escalationConnection")}${cycle}`;
    default:
      return source;
  }
}
