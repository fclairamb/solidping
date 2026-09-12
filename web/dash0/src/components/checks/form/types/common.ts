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

// Config keys the SHARED form owns rather than any type module: they are
// seeded, edited and serialized by `check-form.tsx` itself (timeout input,
// tunnel selector, IP-version selector). Like a module's `ownedKeys` they are
// excluded from the unmodeled-key passthrough below — the form models them, so
// clearing one of those inputs must actually delete the key.
export const SHARED_FORM_CONFIG_KEYS: readonly string[] = [
  "timeout",
  "tunnelCheckUid",
  "ipVersion",
];

// PassthroughSource is the config the check form was seeded from, tagged with
// the check type it belongs to.
export interface PassthroughSource {
  type: CheckType;
  config: CheckConfig;
}

// passthroughConfigFor gates the unmodeled-key passthrough on the source still
// describing the ACTIVE type.
//
// Without this gate, switching type on the form would carry the previous type's
// unmodeled keys into the new type's payload — an `http` check's `body` and
// `headers_pattern` landing in a `tcp` config the checker would reject. The
// form also resets the source on a type switch; this is the second half of the
// same contract, and the half a unit test can reach.
export function passthroughConfigFor(
  source: PassthroughSource | undefined,
  activeType: CheckType,
): CheckConfig | undefined {
  if (!source || source.type !== activeType) return undefined;
  return source.config;
}

// assembleSubmittedConfig builds the config the form previews AND submits.
//
// The server's PATCH-merge uses REPLACE semantics for public keys: a public key
// missing from the submitted config is dropped from the stored one. Every type
// module rebuilds its config from its own state, so before spec 2026-09-11-01
// every public key the module did not model — an HTTP check's request `body`,
// its plain `headers`, `body_expect`, … — was silently deleted the first time
// the check was saved from the dashboard.
//
// The server cannot tell "the module deliberately cleared this key" from "the
// module has never heard of this key"; only the form can. So each module
// DECLARES the keys it models (`ownedKeys`) and everything else found in the
// config the form was seeded from is carried through untouched:
//
//   { ...passthrough, ...moduleConfig, ...sharedConfig }
//
// Omit-to-clear therefore still works for every key a module owns (it is
// excluded from the passthrough, so an omitted owned key really is absent),
// while a key nobody models survives the round-trip byte-for-byte.
//
// `secretFields` (server-declared, from the check-types metadata) is excluded
// too. Secrets never come back on a read today, so they cannot normally be in
// `initialConfig`; excluding them anyway means a deployment that ever did
// return them could not make the passthrough re-send a credential the operator
// never touched, which would defeat the dirty-flag contract the secret editors
// rely on.
export function assembleSubmittedConfig(params: {
  /** The config the form was seeded from (the stored check, or an applied sample). */
  initialConfig?: CheckConfig;
  /** Keys the active module reads in `fromConfig` or writes in `toConfig`. */
  ownedKeys: readonly string[];
  /** Keys the active type stores encrypted, from the check-types metadata. */
  secretFields?: readonly string[];
  /** What the active module's `toConfig` produced. */
  moduleConfig: CheckConfig;
  /** Shared-form keys (timeout / tunnelCheckUid / ipVersion). */
  sharedConfig?: CheckConfig;
}): CheckConfig {
  const { initialConfig, ownedKeys, secretFields, moduleConfig, sharedConfig } =
    params;
  const out: CheckConfig = {};
  if (initialConfig) {
    const excluded = new Set<string>([
      ...ownedKeys,
      ...SHARED_FORM_CONFIG_KEYS,
      ...(secretFields ?? []),
    ]);
    for (const [key, value] of Object.entries(initialConfig)) {
      if (excluded.has(key)) continue;
      out[key] = value;
    }
  }
  Object.assign(out, moduleConfig);
  if (sharedConfig) Object.assign(out, sharedConfig);
  return out;
}

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
