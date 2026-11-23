/**
 * The target a check probes, as shown in the checks list and the check
 * header: its `url` when it has one, else its `host` followed by its `port`
 * (`localhost:5432`). Types with neither (heartbeat, email, …) have none.
 *
 * Host/port checkers (tcp, postgres, mysql, redis, ssh, …) keep the port in a
 * separate `port` config field, so showing the host alone hid which service
 * was probed: two checks on `localhost:5432` and `localhost:6379` both read
 * `localhost` (issue #998).
 */
export function checkTargetOf(config: Record<string, unknown> | undefined): string | undefined {
  const url = config?.url;
  if (typeof url === "string" && url) return url;

  const host = config?.host;
  if (typeof host !== "string" || !host) return undefined;

  const port = portOf(config?.port);
  if (port === undefined || hasPort(host)) return host;

  // A bare IPv6 literal needs brackets, or its port reads as one more group.
  const shown = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
  return `${shown}:${port}`;
}

/** A usable port number from a config value (number or numeric string). */
function portOf(value: unknown): number | undefined {
  const port = typeof value === "string" && value.trim() !== "" ? Number(value) : value;
  if (typeof port !== "number" || !Number.isInteger(port) || port <= 0) return undefined;
  return port;
}

/** Whether `host` already carries a port: `host:port` or `[v6]:port`. */
function hasPort(host: string): boolean {
  if (host.startsWith("[")) return /\]:\d+$/.test(host);
  return /^[^:]+:\d+$/.test(host);
}
