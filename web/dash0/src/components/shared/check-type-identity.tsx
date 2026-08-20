// CHECK_TYPE_IDENTITY is the one canonical visual identity for a check
// type — label, tint tone, and icon — used everywhere a check type is
// rendered: the checks list chip, the check detail page, and the new-check
// type picker. It replaces the old `ProtocolBadge` if-chain
// (checks.index.tsx) and the duplicated `PROTOCOL_BADGE_TONES` copy
// (design-reference.tsx), which together covered only 5 of the ~40
// creatable types.
//
// Keyed by the RAW BACKEND check-type string (server/internal/checkers/
// checkerdef/types.go), same rationale as check-type-docs-anchors.ts: the
// backend registry is the source of truth and can carry types (e.g.
// `kubernetes`) that aren't creatable in this form yet. Legacy alias
// strings that may still appear in stored data (`https`, `ping`, `ssl`'s
// sibling `tls`) resolve to the same identity as their canonical type.
//
// Icons are Lucide today; the `icon` field's type accepts any component
// rendering an SVG that inherits `currentColor` on a square viewBox, so an
// internally designed icon set (24x24, ~2px stroke) can replace entries
// here later with no call-site changes.
import type { ComponentType, SVGProps } from "react";
import {
  Activity,
  AppWindow,
  Boxes,
  Cable,
  CalendarClock,
  ChartColumn,
  Clock,
  Container,
  Database,
  DatabaseZap,
  EthernetPort,
  FileCode,
  Flame,
  FolderLock,
  FolderOpen,
  Gamepad2,
  Globe,
  HeartPulse,
  Inbox,
  Leaf,
  Lock,
  Logs,
  MailCheck,
  MonitorDot,
  Moon,
  Pickaxe,
  PhoneCall,
  Rabbit,
  Radar,
  Radio,
  Router,
  Rss,
  Send,
  ShieldAlert,
  Signpost,
  SquareTerminal,
  Wifi,
  Workflow,
  type LucideIcon,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export type CheckTypeIconComponent = LucideIcon | ComponentType<SVGProps<SVGSVGElement>>;

export interface CheckTypeIdentity {
  /** Display label, e.g. "HTTP", "PostgreSQL". */
  label: string;
  /** Tailwind tint classes: `bg-*-500/10 text-*-700 dark:text-*-400 border-*-500/25`. */
  tone: string;
  /** Custom-SVG slot-in later — anything rendering currentColor on a square viewBox. */
  icon: CheckTypeIconComponent;
}

// Tone strings, one per family, shared by every member of that family so the
// class string is never hand-typed twice. The shape (`bg-{hue}-500/10
// text-{hue}-700 dark:text-{hue}-400 border-{hue}-500/25`) is asserted by the
// drift-guard test.
//
// The first five are the ORIGINALLY SHIPPED tones (ProtocolBadge /
// PROTOCOL_BADGE_TONES) and must not change — they're in users' muscle
// memory. The rest extend the same shape to a new hue per family; none of
// them collide with the status colors (green=ok / red=down stay reserved).
const TONE_BLUE = "bg-blue-500/10 text-blue-700 dark:text-blue-400 border-blue-500/25"; // shipped: http/https
const TONE_CYAN = "bg-cyan-500/10 text-cyan-700 dark:text-cyan-400 border-cyan-500/25"; // shipped: tcp
const TONE_AMBER = "bg-amber-500/10 text-amber-700 dark:text-amber-400 border-amber-500/25"; // shipped: dns
const TONE_PURPLE =
  "bg-purple-500/10 text-purple-700 dark:text-purple-400 border-purple-500/25"; // shipped: icmp/ping
const TONE_EMERALD =
  "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400 border-emerald-500/25"; // shipped: tls/ssl
const TONE_TEAL = "bg-teal-500/10 text-teal-700 dark:text-teal-400 border-teal-500/25"; // remote access
const TONE_ROSE = "bg-rose-500/10 text-rose-700 dark:text-rose-400 border-rose-500/25"; // mail
const TONE_INDIGO =
  "bg-indigo-500/10 text-indigo-700 dark:text-indigo-400 border-indigo-500/25"; // databases
const TONE_FUCHSIA =
  "bg-fuchsia-500/10 text-fuchsia-700 dark:text-fuchsia-400 border-fuchsia-500/25"; // messaging/RPC
const TONE_LIME = "bg-lime-500/10 text-lime-700 dark:text-lime-400 border-lime-500/25"; // game
const TONE_SKY = "bg-sky-500/10 text-sky-700 dark:text-sky-400 border-sky-500/25"; // infra
const TONE_SLATE = "bg-slate-500/10 text-slate-700 dark:text-slate-400 border-slate-500/25"; // scripted/synthetic

// CHECK_TYPE_IDENTITY — see the family → tone table on the design reference
// page (`/orgs/$org/design-reference#check-type-badge`) for the human-facing
// version of this grouping.
export const CHECK_TYPE_IDENTITY: Partial<Record<string, CheckTypeIdentity>> = {
  // Web — shipped blue, extended to the rest of the browser-facing family.
  http: { label: "HTTP", tone: TONE_BLUE, icon: Globe },
  https: { label: "HTTP", tone: TONE_BLUE, icon: Globe }, // legacy alias
  websocket: { label: "WebSocket", tone: TONE_BLUE, icon: Cable },
  browser: { label: "Browser", tone: TONE_BLUE, icon: AppWindow },

  // Raw network — shipped cyan, extended to the rest of the raw-socket family.
  tcp: { label: "TCP", tone: TONE_CYAN, icon: EthernetPort },
  udp: { label: "UDP", tone: TONE_CYAN, icon: Radio },
  ntp: { label: "NTP", tone: TONE_CYAN, icon: Clock },
  snmp: { label: "SNMP", tone: TONE_CYAN, icon: Router },

  // Naming — shipped amber, extended to the rest of the naming/expiry family.
  dns: { label: "DNS", tone: TONE_AMBER, icon: Signpost },
  domain: { label: "Domain", tone: TONE_AMBER, icon: CalendarClock },
  dnsbl: { label: "DNSBL", tone: TONE_AMBER, icon: ShieldAlert },

  // Reachability — shipped purple.
  icmp: { label: "ICMP", tone: TONE_PURPLE, icon: Radar },
  ping: { label: "ICMP", tone: TONE_PURPLE, icon: Radar }, // legacy alias

  // Certificates — shipped emerald.
  ssl: { label: "TLS", tone: TONE_EMERALD, icon: Lock },
  tls: { label: "TLS", tone: TONE_EMERALD, icon: Lock }, // legacy alias

  // Remote access.
  ssh: { label: "SSH", tone: TONE_TEAL, icon: SquareTerminal },
  sftp: { label: "SFTP", tone: TONE_TEAL, icon: FolderLock },
  ftp: { label: "FTP", tone: TONE_TEAL, icon: FolderOpen },
  rdp: { label: "RDP", tone: TONE_TEAL, icon: MonitorDot },

  // Mail.
  smtp: { label: "SMTP", tone: TONE_ROSE, icon: Send },
  pop3: { label: "POP3", tone: TONE_ROSE, icon: Inbox },
  imap: { label: "IMAP", tone: TONE_ROSE, icon: Inbox },
  email: { label: "Email", tone: TONE_ROSE, icon: MailCheck },

  // Databases.
  postgresql: { label: "PostgreSQL", tone: TONE_INDIGO, icon: Database },
  mysql: { label: "MySQL", tone: TONE_INDIGO, icon: Database },
  mssql: { label: "MSSQL", tone: TONE_INDIGO, icon: Database },
  oracle: { label: "Oracle", tone: TONE_INDIGO, icon: Database },
  clickhouse: { label: "ClickHouse", tone: TONE_INDIGO, icon: ChartColumn },
  redis: { label: "Redis", tone: TONE_INDIGO, icon: DatabaseZap },
  mongodb: { label: "MongoDB", tone: TONE_INDIGO, icon: Leaf },

  // Messaging / RPC.
  grpc: { label: "gRPC", tone: TONE_FUCHSIA, icon: Workflow },
  kafka: { label: "Kafka", tone: TONE_FUCHSIA, icon: Logs },
  mqtt: { label: "MQTT", tone: TONE_FUCHSIA, icon: Rss },
  rabbitmq: { label: "RabbitMQ", tone: TONE_FUCHSIA, icon: Rabbit },

  // Game.
  a2s: { label: "A2S Game Server", tone: TONE_LIME, icon: Gamepad2 },
  minecraft: { label: "Minecraft", tone: TONE_LIME, icon: Pickaxe },

  // Infra.
  docker: { label: "Docker", tone: TONE_SKY, icon: Container },
  prometheus: { label: "Prometheus", tone: TONE_SKY, icon: Flame },
  freebox_line: { label: "Freebox Line", tone: TONE_SKY, icon: Wifi },
  // Not yet creatable in this form (see check-type-docs-anchors.ts), but has
  // a backend checker and a docs section — the drift guard requires an
  // identity for it regardless.
  kubernetes: { label: "Kubernetes", tone: TONE_SKY, icon: Boxes },

  // Scripted / synthetic.
  js: { label: "JavaScript", tone: TONE_SLATE, icon: FileCode },
  sleep: { label: "Sleep", tone: TONE_SLATE, icon: Moon },
  heartbeat: { label: "Heartbeat", tone: TONE_SLATE, icon: HeartPulse },
  sip: { label: "SIP", tone: TONE_SLATE, icon: PhoneCall },
};

// FALLBACK_TONE is deliberately "" — an unrecognized type gets the plain
// outline badge (no tint classes at all), never an invented color.
const FALLBACK_TONE = "";

/**
 * getCheckTypeIdentity never returns undefined: an unknown/unmapped type
 * falls back to a plain outline badge, its raw name as label, and the
 * neutral Activity icon — never an invented sixth (er, thirteenth) style.
 */
export function getCheckTypeIdentity(type: string | undefined): CheckTypeIdentity {
  if (!type) {
    return { label: "Unknown", tone: FALLBACK_TONE, icon: Activity };
  }
  const known = CHECK_TYPE_IDENTITY[type.toLowerCase()];
  if (known) return known;
  return { label: type, tone: FALLBACK_TONE, icon: Activity };
}

/**
 * iconToneClassName extracts just the text-color classes out of a tone
 * string (dropping `bg-*` and `border-*`, which belong on a badge chip, not
 * a bare icon) — so a leading icon renders in the same hue as its badge
 * without painting a translucent background box behind the glyph.
 */
export function iconToneClassName(tone: string): string {
  return tone
    .split(" ")
    .filter((cls) => cls.includes("text-"))
    .join(" ");
}

const CHECK_TYPE_BADGE_BASE = "text-[10px] font-mono font-medium uppercase px-1.5 py-0.5";

/**
 * CheckTypeBadge is the canonical rendering of "which check type is this" —
 * the 10px mono uppercase chip used in the checks list and the check detail
 * page. Registry-driven, so both the old `ProtocolBadge` if-chain and the
 * design-reference `PROTOCOL_BADGE_TONES` copy collapse into this one
 * component. Deliberately text-only — no icon — even where a caller also
 * renders a leading `CheckTypeIcon` alongside it; at 10px an icon inside the
 * chip itself is noise, and the label is the signal. The tint is decoration
 * layered on the label; the label alone is never dropped.
 */
export function CheckTypeBadge({
  type,
  className,
}: {
  type?: string;
  className?: string;
}) {
  const identity = getCheckTypeIdentity(type);
  return (
    <Badge
      variant="outline"
      className={cn(CHECK_TYPE_BADGE_BASE, identity.tone, className)}
      title={type}
    >
      {identity.label}
    </Badge>
  );
}

/**
 * CheckTypeIcon renders a check type's registry icon, tinted to its tone
 * (or the neutral default color for an unmapped type). Meant as a leading
 * glyph next to a label/badge — e.g. the type-picker row, the check detail
 * header — never inside the 10px `CheckTypeBadge` chip itself.
 */
export function CheckTypeIcon({
  type,
  className,
}: {
  type?: string;
  className?: string;
}) {
  const identity = getCheckTypeIdentity(type);
  const Icon = identity.icon;
  return (
    <Icon
      className={cn("h-4 w-4 shrink-0", iconToneClassName(identity.tone), className)}
      aria-hidden="true"
    />
  );
}
