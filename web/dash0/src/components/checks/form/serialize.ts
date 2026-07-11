// Single-source config serialization support for the check form.
//
// Historically the form carried the state→config mapping TWICE: once in the
// `currentConfig` useMemo (feeding the live validation preview) and again,
// duplicated, inside `handleSubmit` (building the submitted payload). Two
// copies is a standing invitation for the preview and the persisted payload to
// disagree. The form now serializes ONCE via `currentConfig` and reuses it for
// both preview and submit; this module owns the remaining submit-only concern:
// per-type required-field validation.

type Cfg = Record<string, unknown>;

// Types whose only required field is `host`, all sharing the same message.
const HOST_REQUIRED = new Set<string>([
  "ssl",
  "ntp",
  "rdp",
  "tcp",
  "udp",
  "ftp",
  "ssh",
  "sftp",
  "pop3",
  "imap",
  "icmp",
  "smtp",
  "postgresql",
  "mysql",
  "mssql",
  "oracle",
  "redis",
  "mongodb",
  "rabbitmq",
  "grpc",
  "mqtt",
  "a2s",
  "minecraft",
  "snmp",
]);

// requiredFieldError validates the already-serialized `config` for the active
// check type and returns a human error string, or null when it is submittable.
// It reads only the serialized config (plus `configPrivateKeys` for the SIP
// stored-password case), so it stays in lockstep with `currentConfig`.
export function requiredFieldError(
  type: string,
  config: Cfg,
  configPrivateKeys?: string[],
): string | null {
  if (HOST_REQUIRED.has(type)) {
    return config.host ? null : "Host is required";
  }
  switch (type) {
    case "http":
    case "websocket":
    case "browser":
      return config.url ? null : "URL is required";
    case "dns":
      return config.host ? null : "Domain is required";
    case "domain":
      return config.domain ? null : "Domain is required";
    case "js":
      return config.script ? null : "Script is required";
    case "kafka":
      return config.brokers ? null : "Brokers are required";
    case "dnsbl":
      return config.target ? null : "Target IP or hostname is required";
    case "sleep":
      return config.sleep_ms !== undefined
        ? null
        : "Sleep duration is required";
    case "docker":
      return config.containerName || config.containerId
        ? null
        : "Container name or ID is required";
    case "freebox_line":
      if (!config.connectionUid) return "Freebox connection is required";
      if (config.linkType !== "xdsl" && config.linkType !== "ftth")
        return "Link type must be xdsl or ftth";
      return null;
    case "sip":
      if (!config.host) return "Host is required";
      if (config.mode === "register") {
        if (!config.username) return "Username is required for register mode";
        if (!config.password && !configPrivateKeys?.includes("password"))
          return "Password is required for register mode";
      }
      return null;
    default:
      // heartbeat, email, and any type with no required field.
      return null;
  }
}
