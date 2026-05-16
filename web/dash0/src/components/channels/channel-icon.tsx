import {
  Bell,
  Hash,
  Mail,
  MessageCircle,
  MessageSquare,
  MessagesSquare,
  Webhook,
} from "lucide-react";
import type { ConnectionType } from "@/api/hooks";

interface ChannelIconProps {
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
  ntfy: Bell,
  opsgenie: Bell,
  pushover: Bell,
};

export function ChannelIcon({ type, className }: ChannelIconProps) {
  const Icon = ICONS[type] ?? Webhook;
  return <Icon className={className} aria-hidden="true" />;
}

export function channelIconComponent(type: ConnectionType) {
  return ICONS[type] ?? Webhook;
}

export function channelLabel(type: ConnectionType): string {
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
    case "ntfy":
      return "ntfy";
    case "opsgenie":
      return "Opsgenie";
    case "pushover":
      return "Pushover";
    default:
      return type;
  }
}
