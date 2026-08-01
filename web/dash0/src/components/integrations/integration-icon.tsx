import {
  Bell,
  BellRing,
  Boxes,
  Hash,
  Mail,
  MessageCircle,
  MessageSquare,
  MessagesSquare,
  Phone,
  Router,
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
