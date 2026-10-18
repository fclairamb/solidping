// The org-less "monitor this" deep link (spec 2026-10-14-03).
//
// `/d/monitor?checkType=http&httpUrl=…` is what the website's free tools link
// to. The visitor may be signed out, may have no org yet, and may confirm the
// registration email in another tab, so the prefill is kept in localStorage
// until the first authenticated landing consumes it.
//
// Nothing here creates a check: the prefill only fills the new-check form.

/** The prefill keys `/orgs/$org/checks/new` reads. Anything else is dropped. */
export const MONITOR_QUERY_KEYS = [
  "checkType",
  "checkPeriod",
  "checkName",
  "checkSlug",
  "httpUrl",
  "httpMethod",
  "host",
  "port",
  "url",
  "domain",
  "username",
  "database",
  "expectedStatus",
  "timeout",
  "label",
  "region",
  "group",
  "confirmationPeriod",
  "recoveryPeriod",
  "section",
] as const;

export type MonitorQuery = Partial<
  Record<(typeof MONITOR_QUERY_KEYS)[number], string | number | string[]>
>;

const PENDING_KEY = "solidping_pending_check";
export const PENDING_CHECK_TTL_MS = 24 * 60 * 60 * 1000;

/** Keeps only the allowlisted keys, with string/number (or string-list) values. */
export function filterMonitorQuery(
  search: Record<string, unknown>,
): MonitorQuery {
  const out: Record<string, string | number | string[]> = {};
  for (const key of MONITOR_QUERY_KEYS) {
    const value = search[key];
    if (typeof value === "string" || typeof value === "number") {
      out[key] = value;
    } else if (
      Array.isArray(value) &&
      value.length > 0 &&
      value.every((v) => typeof v === "string")
    ) {
      out[key] = value as string[];
    }
  }
  return out as MonitorQuery;
}

/** The `returnTo` the /monitor route hands to /login: itself, with its query. */
export function monitorReturnTo(basepath: string, query: MonitorQuery): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined) continue;
    for (const v of Array.isArray(value) ? value : [value]) {
      params.append(key, String(v));
    }
  }
  const qs = params.toString();
  return `${basepath}/monitor${qs ? `?${qs}` : ""}`;
}

/** True when `returnTo` is the /monitor route under the base path (query allowed). */
export function isMonitorReturnTo(
  returnTo: string | undefined | null,
  basepath: string,
): returnTo is string {
  if (!returnTo) return false;
  const path = `${basepath}/monitor`;
  return returnTo === path || returnTo.startsWith(`${path}?`);
}

export function savePendingCheck(query: MonitorQuery, now = Date.now()): void {
  if (Object.keys(query).length === 0) return;
  try {
    localStorage.setItem(
      PENDING_KEY,
      JSON.stringify({ query, expiresAt: now + PENDING_CHECK_TTL_MS }),
    );
  } catch {
    // Storage blocked: the prefill is a convenience, the flow still works.
  }
}

export function clearPendingCheck(): void {
  try {
    localStorage.removeItem(PENDING_KEY);
  } catch {
    // ignore
  }
}

/** Returns the pending prefill once (and deletes it), or null when none/expired. */
export function consumePendingCheck(now = Date.now()): MonitorQuery | null {
  try {
    const raw = localStorage.getItem(PENDING_KEY);
    if (!raw) return null;
    localStorage.removeItem(PENDING_KEY);
    const parsed = JSON.parse(raw) as { query?: unknown; expiresAt?: unknown };
    if (typeof parsed.expiresAt !== "number" || parsed.expiresAt <= now) {
      return null;
    }
    if (!parsed.query || typeof parsed.query !== "object") return null;
    const query = filterMonitorQuery(parsed.query as Record<string, unknown>);
    return Object.keys(query).length > 0 ? query : null;
  } catch {
    return null;
  }
}

export type OrgLanding =
  | { to: "/orgs/$org"; params: { org: string } }
  | {
      to: "/orgs/$org/checks/new";
      params: { org: string };
      search: Record<string, unknown>;
    };

/**
 * Where to land in `org` after an authenticated entry: the prefilled new-check
 * form when a pending prefill exists (consumed here), the org root otherwise.
 * The demo org never receives it.
 */
export function landingForOrg(org: string, isDemo = false): OrgLanding {
  if (!isDemo) {
    const query = consumePendingCheck();
    if (query) {
      return { to: "/orgs/$org/checks/new", params: { org }, search: query };
    }
  }
  return { to: "/orgs/$org", params: { org } };
}
