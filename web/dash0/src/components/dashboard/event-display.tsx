import { Calendar, Cpu, Rocket } from "lucide-react";

export function getEventIcon(eventType?: string) {
  if (!eventType) return <Calendar className="h-4 w-4" />;

  if (eventType.startsWith("check.")) {
    return <Cpu className="h-4 w-4 text-blue-400" />;
  }
  if (eventType === "incident.resolved") {
    return <Calendar className="h-4 w-4 text-green-500" />;
  }
  if (eventType.startsWith("incident.")) {
    return <Calendar className="h-4 w-4 text-yellow-500" />;
  }
  if (eventType.startsWith("org.activation.")) {
    return <Rocket className="h-4 w-4 text-purple-500" />;
  }
  return <Calendar className="h-4 w-4" />;
}

export function getEventLabel(
  eventType: string | undefined,
  t: (key: string, options?: Record<string, unknown>) => string,
): string {
  if (!eventType) return t("unknown");
  return t(`types.${eventType}`, { defaultValue: eventType });
}

// getEventDescription returns a human-readable description for an event type,
// or "" when the type has no description. Callers should skip rendering the
// description line when this returns an empty string.
export function getEventDescription(
  eventType: string | undefined,
  t: (key: string, options?: Record<string, unknown>) => string,
): string {
  if (!eventType) return "";
  return t(`descriptions.${eventType}`, { defaultValue: "" });
}

// getEventCheckName returns the check's name captured in the event payload when
// the event was recorded, falling back to the check's slug for older events
// that only captured check_slug (no check_name). Returns undefined only when
// the payload has neither — fully historical events with no check
// identification at all, or events unrelated to a check.
export function getEventCheckName(event: {
  payload?: Record<string, unknown>;
}): string | undefined {
  const name = event.payload?.check_name;
  if (typeof name === "string" && name.length > 0) return name;

  const slug = event.payload?.check_slug;
  return typeof slug === "string" && slug.length > 0 ? slug : undefined;
}

// getEventChannelName returns the notification channel name captured in the
// event payload (set on org.activation.first_notification_configured events
// emitted after this field was introduced), or undefined for historical
// events that predate it.
export function getEventChannelName(event: {
  payload?: Record<string, unknown>;
}): string | undefined {
  const name = event.payload?.channel_name;
  return typeof name === "string" && name.length > 0 ? name : undefined;
}

// getEventChannelUid returns the notification channel UID captured in the
// event payload, or undefined when absent (historical events, or the
// channel-name lookup failing for any other reason).
export function getEventChannelUid(event: {
  payload?: Record<string, unknown>;
}): string | undefined {
  const uid = event.payload?.channel_uid;
  return typeof uid === "string" && uid.length > 0 ? uid : undefined;
}
