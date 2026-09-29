/**
 * Super-admin impersonation, client side (spec 2026-09-29-03).
 *
 * The impersonation token lives in THIS TAB'S sessionStorage, never in
 * localStorage and never in the URL. The admin's own session (access token,
 * refresh token, expiry) stays exactly where it was in localStorage, so:
 *
 *  - "Exit" is just dropping the sessionStorage entry: the next request goes
 *    out with the admin's own token again;
 *  - the admin's other tabs are not affected at all;
 *  - closing the tab ends the impersonation.
 *
 * `getToken()` in api/client.ts prefers this token when present, which is what
 * makes every request (REST and the live socket) act as the target.
 *
 * The token has no refresh token and a fixed 30-minute lifetime. When it runs
 * out, the refresh path exits the impersonation instead of refreshing (see
 * token-refresh.ts), so the admin lands back on their own session.
 */
import { DASH_BASE } from "@/lib/base-path";

export const IMPERSONATION_KEY = "solidping_impersonation";

export interface ImpersonationSession {
  /** The impersonation access token. */
  accessToken: string;
  /** Epoch ms at which the token stops working. */
  expiresAt: number;
  /** Who is being impersonated, for the banner. */
  targetEmail: string;
  /** The org the token is scoped to. */
  orgSlug: string;
  /** Dashboard path (without the /d prefix) to return to on exit. */
  returnPath: string;
}

function storage(): Storage | null {
  try {
    return typeof window === "undefined" ? null : window.sessionStorage;
  } catch {
    return null;
  }
}

function isSession(value: unknown): value is ImpersonationSession {
  if (!value || typeof value !== "object") return false;
  const v = value as Record<string, unknown>;
  return (
    typeof v.accessToken === "string" &&
    v.accessToken !== "" &&
    typeof v.expiresAt === "number" &&
    typeof v.targetEmail === "string" &&
    typeof v.orgSlug === "string" &&
    typeof v.returnPath === "string" &&
    v.returnPath.startsWith("/")
  );
}

/** Stores the impersonation for this tab. */
export function startImpersonation(session: ImpersonationSession): void {
  storage()?.setItem(IMPERSONATION_KEY, JSON.stringify(session));
}

/** The impersonation active in this tab, or null. Malformed entries are dropped. */
export function getImpersonation(): ImpersonationSession | null {
  const store = storage();
  const raw = store?.getItem(IMPERSONATION_KEY);
  if (!raw) return null;

  try {
    const parsed: unknown = JSON.parse(raw);
    if (isSession(parsed)) return parsed;
  } catch {
    // fall through
  }

  store?.removeItem(IMPERSONATION_KEY);
  return null;
}

/** The impersonation access token for this tab, or null. */
export function getImpersonationToken(): string | null {
  return getImpersonation()?.accessToken ?? null;
}

export function isImpersonating(): boolean {
  return getImpersonation() !== null;
}

/** True once the impersonation token has run out. */
export function isImpersonationExpired(
  session: ImpersonationSession,
  now: number = Date.now(),
): boolean {
  return now >= session.expiresAt;
}

/** Drops the impersonation without navigating. Returns what was active. */
export function clearImpersonation(): ImpersonationSession | null {
  const session = getImpersonation();
  storage()?.removeItem(IMPERSONATION_KEY);
  return session;
}

/**
 * Where exiting lands: the page the admin started from. The stored path is
 * our own, but it is still checked to be a same-app absolute path so a
 * tampered entry cannot send the browser off-site.
 */
export function exitDestination(session: ImpersonationSession | null): string {
  const path = session?.returnPath;
  if (!path || !path.startsWith("/") || path.startsWith("//")) {
    return `${DASH_BASE}/`;
  }
  return `${DASH_BASE}${path}`;
}

/**
 * Ends the impersonation and reloads the dashboard on the admin's own
 * session. A full navigation rather than a router push: every cached query,
 * the live socket and the auth context were built as the target and must all
 * be rebuilt as the admin.
 */
export function exitImpersonation(): void {
  const session = clearImpersonation();
  window.location.assign(exitDestination(session));
}

/**
 * Whether the current super admin may impersonate this row (spec
 * 2026-09-29-03). Mirrors the server's refusals so the button is never offered
 * for a request that would be refused: not for super admins, not for
 * yourself, not for the shared demo account, not for someone with no org.
 */
export function canImpersonate(
  row: { uid: string; superAdmin: boolean; demo: boolean; orgs: readonly unknown[] },
  currentUser: { uid: string; isSuperAdmin: boolean } | null,
): boolean {
  if (!currentUser?.isSuperAdmin) return false;
  if (row.uid === currentUser.uid) return false;
  if (row.superAdmin || row.demo) return false;
  return row.orgs.length > 0;
}
