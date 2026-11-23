/**
 * Diagnostics for sessions that vanish (solidping-business#107, spec
 * 2026-11-23-03): on iOS Safari some users land back on the `default` org's
 * login page, signed out, a minute after signing in. Three causes fit and
 * these events tell them apart in production:
 *
 * (a) another storage context (in-app browser, new tab after OAuth, ITP
 *     partitioning): the session is missing and nothing ever cleared it here,
 *     so `session_missing_on_load` carries no `lastClearReason`;
 * (b) WebKit failing to write or read `localStorage`: `session_persist_failed`
 *     fires, or `storageWritable` is false on the missing-session event;
 * (c) a code path clearing the session: `session_cleared` fires with the
 *     caller's reason, and the next `session_missing_on_load` repeats it.
 *
 * Nothing here ever reports a token, a refresh token or an org slug: only
 * whether each key is present.
 *
 * This module must not import `@/api/client` (which imports it), hence the
 * storage keys live here and client.ts takes them from this file.
 */
import { captureEventQueued } from "@/lib/analytics";
import { SESSION_ORG_KEY } from "@/lib/session-org";

export const TOKEN_KEY = "solidping_session_token";
export const REFRESH_TOKEN_KEY = "solidping_refresh_token";

/**
 * Where the last `clearToken` call came from, kept in `localStorage` so the
 * page load that follows a clear can still say why the session is gone. A
 * successful `setSession` removes it.
 */
export const SESSION_CLEARED_KEY = "solidping_session_cleared";

export type SessionClearReason =
  | "logout"
  | "api-401"
  | "auth-me-rejected"
  | "refresh-no-refresh-token"
  | "refresh-rejected"
  | "org-deletion-swap";

export type SessionMissingWhere = "root-redirect" | "org-login";

type ReadableStorage = Pick<Storage, "getItem">;
type WritableStorage = Pick<Storage, "getItem" | "setItem" | "removeItem">;

interface ClearMarker {
  reason: string;
  at: number;
}

function safeStorage(): WritableStorage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    // Some WebKit privacy modes throw on the mere `localStorage` access.
    return null;
  }
}

function readClearMarker(storage: ReadableStorage): ClearMarker | null {
  try {
    const raw = storage.getItem(SESSION_CLEARED_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<ClearMarker>;
    if (typeof parsed.reason !== "string" || typeof parsed.at !== "number") return null;
    return { reason: parsed.reason, at: parsed.at };
  } catch {
    return null;
  }
}

/** True when `storage` accepts a write and returns it. */
export function isStorageWritable(storage: WritableStorage | null): boolean {
  if (!storage) return false;
  const probe = "solidping_storage_probe";
  try {
    storage.setItem(probe, "1");
    const ok = storage.getItem(probe) === "1";
    storage.removeItem(probe);
    return ok;
  } catch {
    return false;
  }
}

/**
 * Records that the session was cleared and why: a `session_cleared` event and
 * a marker the next page load reads. Never throws — diagnostics must not break
 * the clear itself.
 */
export function recordSessionCleared(
  reason: SessionClearReason,
  storage: WritableStorage | null = safeStorage(),
  now: number = Date.now(),
): void {
  try {
    storage?.setItem(SESSION_CLEARED_KEY, JSON.stringify({ reason, at: now }));
  } catch {
    // Storage full or unavailable: the event below still says it.
  }
  captureEventQueued("session_cleared", { reason });
}

/** Drops the clear marker once a new session is stored. */
export function forgetSessionCleared(storage: WritableStorage | null = safeStorage()): void {
  try {
    storage?.removeItem(SESSION_CLEARED_KEY);
  } catch {
    // Nothing to do: a stale marker only makes a later report less precise.
  }
}

/** Reports a `localStorage` write that threw while storing a session. */
export function recordSessionPersistFailed(error: unknown): void {
  captureEventQueued("session_persist_failed", {
    // The name only (QuotaExceededError, SecurityError…): a message could
    // quote the value being written. DOMException is not an Error everywhere.
    errorName:
      error !== null &&
      typeof error === "object" &&
      typeof (error as { name?: unknown }).name === "string"
        ? (error as { name: string }).name
        : typeof error,
  });
}

export interface SessionMissingEnv {
  storage: WritableStorage | null;
  referrer: string;
  hasOpener: boolean;
  standalone: boolean;
  now: number;
}

function referrerOrigin(referrer: string): string | null {
  if (!referrer) return null;
  try {
    return new URL(referrer).origin;
  } catch {
    return null;
  }
}

/**
 * The properties of `session_missing_on_load`: booleans for each session key,
 * never their values, plus what tells a foreign storage context apart.
 */
export function sessionMissingProperties(
  where: SessionMissingWhere,
  env: SessionMissingEnv,
): Record<string, unknown> {
  const { storage } = env;
  const has = (key: string): boolean => {
    try {
      return Boolean(storage?.getItem(key));
    } catch {
      return false;
    }
  };
  const marker = storage ? readClearMarker(storage) : null;
  return {
    where,
    token: has(TOKEN_KEY),
    refresh: has(REFRESH_TOKEN_KEY),
    org: has(SESSION_ORG_KEY),
    storageAvailable: storage !== null,
    storageWritable: isStorageWritable(storage),
    lastClearReason: marker?.reason ?? null,
    lastClearAgeSeconds: marker ? Math.max(0, Math.round((env.now - marker.at) / 1000)) : null,
    referrerOrigin: referrerOrigin(env.referrer),
    hasOpener: env.hasOpener,
    standalone: env.standalone,
  };
}

function isStandalone(): boolean {
  try {
    return (
      (navigator as Navigator & { standalone?: boolean }).standalone === true ||
      window.matchMedia?.("(display-mode: standalone)").matches === true
    );
  } catch {
    return false;
  }
}

function hasWindowOpener(): boolean {
  try {
    return window.opener != null;
  } catch {
    // A cross-origin opener can throw on access; it still is one.
    return true;
  }
}

function currentEnv(): SessionMissingEnv {
  const standalone = isStandalone();
  const hasOpener = hasWindowOpener();
  return {
    storage: safeStorage(),
    referrer: typeof document === "undefined" ? "" : document.referrer,
    hasOpener,
    standalone,
    now: Date.now(),
  };
}

const reported = new Set<SessionMissingWhere>();

/**
 * Captures `session_missing_on_load` once per page load and place. Never
 * throws.
 */
export function reportSessionMissingOnLoad(
  where: SessionMissingWhere,
  env: SessionMissingEnv = currentEnv(),
): void {
  if (reported.has(where)) return;
  reported.add(where);
  try {
    captureEventQueued("session_missing_on_load", sessionMissingProperties(where, env));
  } catch {
    // Diagnostics only.
  }
}

/** Test-only: forget which places already reported. */
export function __resetSessionDiagnosticsForTests(): void {
  reported.clear();
}
