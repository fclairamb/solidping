import {
  Bell,
  BellRing,
  Boxes,
  Hash,
  Mail,
  MessageCircle,
  MessageSquare,
  MessagesSquare,
  MonitorSmartphone,
  Phone,
  Router,
  Send,
  Users,
  Webhook,
  Bot,
} from "lucide-react";
import type { ConnectionType } from "@/api/hooks";

interface IntegrationIconProps {
  type: ConnectionType;
  className?: string;
}

const ICONS: Record<ConnectionType, typeof Webhook> = {
  slack: MessagesSquare,
  discord: MessageCircle,
  webhook: Webhook,
  email: Mail,
  googlechat: MessageSquare,
  mattermost: Hash,
  msteams: Users,
  "msteams-bot": Bot,
  ntfy: Bell,
  opsgenie: Bell,
  pushover: Bell,
  freebox: Router,
  webpush: BellRing,
  kubernetes: Boxes,
  twilio: Phone,
};

export function IntegrationIcon({ type, className }: IntegrationIconProps) {
  const Icon = ICONS[type] ?? Webhook;
  return <Icon className={className} aria-hidden="true" />;
}

export function integrationIconComponent(type: ConnectionType) {
  return ICONS[type] ?? Webhook;
}

export function integrationLabel(type: ConnectionType): string {
  switch (type) {
    case "slack":
      return "Slack";
    case "discord":
      return "Discord";
    case "webhook":
      return "Webhook";
    case "email":
      return "Email";
    case "googlechat":
      return "Google Chat";
    case "mattermost":
      return "Mattermost";
    case "msteams":
      return "Microsoft Teams";
    case "msteams-bot":
      return "Microsoft Teams (bot)";
    case "ntfy":
      return "ntfy";
    case "opsgenie":
      return "Opsgenie";
    case "pushover":
      return "Pushover";
    case "freebox":
      return "Freebox";
    case "webpush":
      return "Browser push";
    case "kubernetes":
      return "Kubernetes";
    case "twilio":
      return "Twilio (SMS / Voice)";
    default:
      return type;
  }
}

/**
 * Icons for the **direct channels** — the per-user contact types that page a
 * person rather than an org connection (`email`, `phone`, `whatsapp`,
 * `telegram`, `slack_user`, `webpush`).
 *
 * They live here, next to the connection-type icons, so one file answers "what
 * does channel X look like" for every channel SolidPing can deliver on. They
 * are deliberately a separate map: a direct channel is NOT a ConnectionType —
 * there is no `telegram` integration row to configure, only an instance-level
 * bot and a per-user connected chat.
 */
const DIRECT_CHANNEL_ICONS: Record<string, typeof Webhook> = {
  email: Mail,
  phone: Phone,
  whatsapp: MessageCircle,
  telegram: Send,
  slack_user: MessageSquare,
  webpush: MonitorSmartphone,
};

/** Returns the icon component for a direct-channel contact type. */
export function directChannelIconComponent(contactType: string) {
  return DIRECT_CHANNEL_ICONS[contactType] ?? BellRing;
}

/** Renders the icon for a direct-channel contact type. */
export function DirectChannelIcon({
  type,
  className,
}: {
  type: string;
  className?: string;
}) {
  const Icon = DIRECT_CHANNEL_ICONS[type] ?? BellRing;

  return <Icon className={className} aria-hidden="true" />;
}

/**
 * Brand-correct label for a direct-channel contact type. Exists for the same
 * reason channel-labels.ts does: CSS `capitalize` would render "Whatsapp".
 */
export function directChannelLabel(contactType: string): string {
  switch (contactType) {
    case "email":
      return "Email";
    case "phone":
      return "Phone (SMS)";
    case "whatsapp":
      return "WhatsApp";
    case "telegram":
      return "Telegram";
    case "slack_user":
      return "Slack DM";
    case "webpush":
      return "Browser push";
    default:
      return contactType;
  }
}
