// Port defaults, common-port suggestions and `host:port` splitting shared by
// every check form that has a host + port pair.

/** Default port written into a NEW check's form, per check type. */
export const DEFAULT_PORTS: Readonly<Record<string, string>> = {
  tcp: "443",
  udp: "53",
  ssh: "22",
  sftp: "22",
  ftp: "21",
  ssl: "443",
};

export interface CommonPort {
  port: string;
  label: string;
}

/** One-click suggestions under the port field. Types absent here show none. */
export const COMMON_PORTS: Readonly<Record<string, readonly CommonPort[]>> = {
  tcp: [
    { port: "22", label: "SSH" },
    { port: "80", label: "HTTP" },
    { port: "443", label: "HTTPS" },
    { port: "3306", label: "MySQL" },
    { port: "5432", label: "PostgreSQL" },
    { port: "6379", label: "Redis" },
    { port: "25", label: "SMTP" },
    { port: "587", label: "Submission" },
  ],
  udp: [
    { port: "53", label: "DNS" },
    { port: "123", label: "NTP" },
    { port: "161", label: "SNMP" },
  ],
};

/**
 * Fills an empty `port` of a freshly seeded form state with the type's default.
 * A port that is already set is never touched.
 */
export function withDefaultPort<S>(type: string, state: S): S {
  const def = DEFAULT_PORTS[type];
  if (!def || typeof state !== "object" || state === null) return state;
  const s = state as { port?: unknown };
  if (typeof s.port !== "string" || s.port !== "") return state;
  return { ...state, port: def };
}

/**
 * On a type switch, the carried-over port follows the new type's default when
 * it was only the previous type's default (443 on tcp -> 22 on ssh). A port the
 * operator chose is kept.
 */
export function portForTypeSwitch<S>(
  prevType: string,
  nextType: string,
  state: S,
): S {
  if (typeof state !== "object" || state === null) return state;
  const s = state as { port?: unknown };
  if (typeof s.port !== "string") return state;
  const prevDefault = DEFAULT_PORTS[prevType];
  if (s.port === "" || (prevDefault !== undefined && s.port === prevDefault)) {
    const def = DEFAULT_PORTS[nextType];
    if (def) return { ...state, port: def };
  }
  return state;
}

const HOST_PORT = /^([^\s:/]+):(\d{1,5})$/;
const BRACKETED_IPV6_PORT = /^(\[[0-9a-f:]+\]):(\d{1,5})$/i;

/**
 * Splits a pasted `host:port` (or `[v6]:port`) into its parts. Returns null
 * when the value is not that shape; a bare IPv6 address is left alone. The
 * bracketed host keeps its brackets only if the caller wants them: we strip
 * them, since the host field stores the bare address.
 */
export function splitHostPort(
  value: string,
): { host: string; port: string } | null {
  const v4 = HOST_PORT.exec(value);
  if (v4) return { host: v4[1], port: v4[2] };
  const v6 = BRACKETED_IPV6_PORT.exec(value);
  if (v6) return { host: v6[1].slice(1, -1), port: v6[2] };
  return null;
}

/**
 * Next form state after the host field changed: a pasted `host:port` is split
 * into the host and port fields, anything else only updates the host.
 */
export function applyHostInput<S extends { host: string; port: string }>(
  state: S,
  value: string,
): S {
  const split = splitHostPort(value);
  return split ? { ...state, ...split } : { ...state, host: value };
}
