// Shared types and helpers for the per-check-type module registry (spec §3).
//
// Each check type is described by a `CheckTypeModule`: `fromConfig` seeds the
// module's local state from a check's config (replacing the old per-type
// `useState` seeding + `applySample`), `toConfig` serializes that state back
// into a config + client-side required-field errors (the SINGLE source for both
// the live preview and the submitted payload), and `Fields` renders the inputs.
import type { FieldError } from "@/hooks/use-check-validation";

export type CheckType =
  | "http"
  | "tcp"
  | "icmp"
  | "dns"
  | "ssl"
  | "heartbeat"
  | "email"
  | "domain"
  | "smtp"
  | "udp"
  | "ssh"
  | "pop3"
  | "imap"
  | "websocket"
  | "postgresql"
  | "mysql"
  | "redis"
  | "mongodb"
  | "ftp"
  | "sftp"
  | "js"
  | "mssql"
  | "oracle"
  | "clickhouse"
  | "grpc"
  | "kafka"
  | "mqtt"
  | "a2s"
  | "minecraft"
  | "rabbitmq"
  | "snmp"
  | "prometheus"
  | "docker"
  | "browser"
  | "freebox_line"
  | "dnsbl"
  | "sip"
  | "ntp"
  | "rdp"
  | "sleep";

export type CheckConfig = Record<string, unknown>;
export type FieldErrors = FieldError[];

/** Props every per-type `Fields` (and the HTTP auth `Fields`) receives. */
export interface CheckTypeFieldsProps<S = unknown> {
  state: S;
  onChange: (next: S) => void;
  errors: FieldErrors;
}

// getConfigField reads a config value as a display string ("" when absent), the
// same coercion the form has always used to seed text inputs.
export function getConfigField(
  config: CheckConfig | undefined,
  field: string,
): string {
  if (!config) return "";
  const value = config[field];
  if (value === undefined || value === null) return "";
  return String(value);
}

// durationStringToSeconds converts a simple Go duration string ("120s", "2m")
// into whole seconds for a numeric input. Returns "" when empty/unparseable so
// the input stays blank rather than showing 0.
export function durationStringToSeconds(raw: string): string {
  if (!raw) return "";
  const match = raw.match(/^(\d+(?:\.\d+)?)(s|m|h)$/);
  if (!match) return "";
  const value = parseFloat(match[1]);
  const unit = match[2];
  const seconds = unit === "h" ? value * 3600 : unit === "m" ? value * 60 : value;
  return String(Math.round(seconds));
}

// splitBlocklists turns the DNSBL blocklists textarea (comma/newline separated)
// into a trimmed, de-duplicated array, dropping empty entries.
export function splitBlocklists(raw: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const z of raw.split(/[\n,]/)) {
    const zone = z.trim();
    if (zone && !seen.has(zone)) {
      seen.add(zone);
      out.push(zone);
    }
  }
  return out;
}
