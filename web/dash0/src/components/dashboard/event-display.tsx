import { Calendar, Cpu, Rocket } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

// Tone classes shared between the per-type registry below and the family
// fallback in getEventTone, so a type that graduates from "family default"
// to "explicit override" (or back) never has to invent a new string.
const TONE_DESTRUCTIVE = "border-destructive/20 bg-destructive/10 text-destructive";
const TONE_EMERALD =
  "border-emerald-500/20 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400";
const TONE_AMBER = "border-amber-500/20 bg-amber-500/10 text-amber-700 dark:text-amber-400";
const TONE_BLUE = "border-blue-500/20 bg-blue-500/10 text-blue-700 dark:text-blue-400";
const TONE_VIOLET = "border-violet-500/20 bg-violet-500/10 text-violet-700 dark:text-violet-400";
const TONE_SLATE = "border-slate-500/20 bg-slate-500/10 text-slate-700 dark:text-slate-400";

// EVENT_TYPE_REGISTRY is the canonical, per-event-type visual identity: one
// emoji and one tone per event type, used everywhere an event is rendered
// (event lists, the dashboard feed, notification lists, the incident
// timeline). It intentionally covers only the event types that need a
// specific pairing — everything else falls back to the coarser per-family
// rules below it (getEventIcon / getEventTone), so an event type shipped
// without a registry entry still renders something reasonable rather than
// nothing.
//
// These pairs are binding product-wide: the backend chat integrations that
// hand-author their own messages (msteamsbot.go, Telegram, Slack) are kept
// aligned to the SAME emoji for the SAME event type — see
// server/internal/notifications/msteamsbot.go and
// server/internal/integrations/telegram/message.go. Do not pick a
// different emoji for one of these types without also updating those
// surfaces.
export const EVENT_TYPE_REGISTRY: Record<string, { emoji: string; tone: string }> = {
  "incident.created": { emoji: "🔴", tone: TONE_DESTRUCTIVE },
  "incident.reopened": { emoji: "🔁", tone: TONE_DESTRUCTIVE },
  "incident.escalated": { emoji: "⚠️", tone: TONE_DESTRUCTIVE },
  "incident.escalation_failed": { emoji: "❌", tone: TONE_DESTRUCTIVE },
  "incident.resolved": { emoji: "🟢", tone: TONE_EMERALD },
  "incident.acknowledged": { emoji: "✅", tone: TONE_AMBER },
  "incident.unacknowledged": { emoji: "↩️", tone: TONE_AMBER },
  "incident.snoozed": { emoji: "💤", tone: TONE_SLATE },
};

// getEventEmoji returns the registry emoji for an event type, or undefined
// for a type with no specific pairing (the family fallback rules have no
// emoji of their own — only a tone and a lucide icon).
export function getEventEmoji(eventType?: string): string | undefined {
  if (!eventType) return undefined;
  return EVENT_TYPE_REGISTRY[eventType]?.emoji;
}

export function getEventIcon(eventType?: string) {
  if (!eventType) return <Calendar className="h-4 w-4" />;

  const emoji = getEventEmoji(eventType);
  if (emoji) {
    return (
      <span className="text-base leading-none" aria-hidden="true">
        {emoji}
      </span>
    );
  }

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

// getEventTone maps an event type to the badge classes for its family, so an
// audit log can be scanned by color before it is read. The tint is decoration
// layered on the translated label — never the only signal — and an
// unrecognized type falls back to the neutral outline badge rather than
// inventing a color. A per-type registry entry (see EVENT_TYPE_REGISTRY)
// wins over the family default when both apply.
//
// Families: failure/escalation reads red, recovery reads emerald, operator
// acknowledgement reads amber, configuration changes read blue, and
// onboarding milestones read violet.
export function getEventTone(eventType?: string): string {
  if (!eventType) return "";

  const registered = EVENT_TYPE_REGISTRY[eventType];
  if (registered) return registered.tone;

  if (
    eventType === "incident.created" ||
    eventType === "incident.opened" ||
    eventType === "incident.reopened" ||
    eventType === "incident.escalated" ||
    eventType === "incident.escalation_failed"
  ) {
    return TONE_DESTRUCTIVE;
  }
  if (eventType === "incident.resolved") {
    return TONE_EMERALD;
  }
  if (eventType.startsWith("incident.")) {
    return TONE_AMBER;
  }
  if (eventType.startsWith("check.") || eventType.startsWith("status_update.")) {
    return TONE_BLUE;
  }
  if (eventType.startsWith("org.activation.")) {
    return TONE_VIOLET;
  }
  return "";
}

// EventTypeBadge is the one canonical rendering of "which event was this" —
// emoji (when the registry has one) + translated label + tone — used
// wherever an event type needs to be shown to an operator: notification
// lists, the notification detail page, the events page, the dashboard feed,
// and the incident timeline. The emoji and tint are decoration layered on
// the label; the label alone is never dropped, so the badge never relies on
// color or emoji as the only signal.
export function EventTypeBadge({
  eventType,
  t,
  className,
}: {
  eventType?: string;
  t: (key: string, options?: Record<string, unknown>) => string;
  className?: string;
}) {
  const emoji = getEventEmoji(eventType);

  return (
    <Badge
      variant="outline"
      className={cn("gap-1.5 text-xs font-medium", getEventTone(eventType), className)}
      title={eventType}
    >
      {emoji && <span aria-hidden="true">{emoji}</span>}
      <span>{getEventLabel(eventType, t)}</span>
    </Badge>
  );
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

// COMMENT_EVENT_TYPE is the event type for user-authored incident comments.
export const COMMENT_EVENT_TYPE = "incident.comment";

// isCommentEvent reports whether an event is a user-authored incident comment
// (as opposed to a system lifecycle event).
export function isCommentEvent(event: { eventType?: string }): boolean {
  return event.eventType === COMMENT_EVENT_TYPE;
}

// getCommentText returns the comment body from the event payload, or "".
export function getCommentText(event: {
  payload?: Record<string, unknown>;
}): string {
  const text = event.payload?.text;
  return typeof text === "string" ? text : "";
}

// getCommentSource returns where a comment originated ("web" | "slack"), or
// undefined for non-comment events / payloads that predate the field.
export function getCommentSource(event: {
  payload?: Record<string, unknown>;
}): "web" | "slack" | undefined {
  const source = event.payload?.source;
  if (source === "slack") return "slack";
  if (source === "web") return "web";
  return undefined;
}

// getCommentSlackAuthor returns the Slack author display name captured in the
// payload (falling back to the Slack user ID), or undefined when neither is
// present. Only meaningful for Slack-sourced comments.
export function getCommentSlackAuthor(event: {
  payload?: Record<string, unknown>;
}): string | undefined {
  const name = event.payload?.slackUserName;
  if (typeof name === "string" && name.length > 0) return name;

  const id = event.payload?.slackUserId;
  return typeof id === "string" && id.length > 0 ? id : undefined;
}
