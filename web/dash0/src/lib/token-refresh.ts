// Single-flight access-token refresh, shared by the proactive timer
// (AuthContext), the reactive 401 handler (api/client.ts), and the live
// socket's 4401 handling (live-socket.ts). All three call the same
// `refreshAccessToken()` — a burst of concurrent callers (e.g. several
// requests 401ing at once, or the proactive timer firing mid-request) shares
// one in-flight POST /api/v1/auth/refresh instead of one each.
//
// Deliberately calls `fetch` directly rather than `apiFetch`: apiFetch's own
// 401 handling would recurse into this module, and refresh failures have
// their own (simpler) fallback — clear the session and let the caller
// decide what to do next (redirect to login).

import { clearToken, getRefreshToken, redirectToExpiredLogin, setSession } from "@/api/client";

interface RefreshResponse {
  accessToken: string;
  expiresIn?: number;
}

/**
 * Why a refresh attempt failed to produce a new access token.
 * - "no-refresh-token": nothing to refresh with — no refresh token stored
 *   (a legacy/partial session, or a login path that skipped setSession).
 * - "rejected": the server answered with a non-OK status — the refresh
 *   token is invalid, revoked, or expired.
 * - "network-error": the fetch itself threw — a transient blip that says
 *   nothing about whether the stored refresh token is still good.
 *
 * The first two mean the app's belief that it's authenticated is simply
 * wrong; the caller should stop acting as if a retry will ever succeed.
 * Only "network-error" is worth retrying quietly.
 */
export type RefreshFailureReason = "no-refresh-token" | "rejected" | "network-error";

export interface RefreshOutcome {
  /** The new access token, or null if the refresh did not succeed. */
  accessToken: string | null;
  /** Set only when accessToken is null. */
  failureReason?: RefreshFailureReason;
}

let inFlight: Promise<RefreshOutcome> | null = null;

/** Logs every refresh failure with its differentiated reason (spec A.5) so
 * a fresh HAR capture is never the only way to diagnose a stuck session.
 * Escalating reasons use console.error so they land in the bug-report ring
 * buffer (components/feedback/errorCollector.ts) for free; a network blip
 * uses console.warn since it's expected and retried quietly. */
function logRefreshFailure(reason: RefreshFailureReason): void {
  const message = `[auth] token refresh failed: ${reason}`;
  if (reason === "network-error") {
    console.warn(message);
  } else {
    console.error(message);
  }
}

/** Session is definitively over — clear it and send the user to login
 * immediately rather than let a caller (proactive timer, socket loop) coast
 * on a token that will never become valid again. */
function escalate(reason: "no-refresh-token" | "rejected"): void {
  logRefreshFailure(reason);
  clearToken();
  redirectToExpiredLogin();
}

async function doRefresh(): Promise<RefreshOutcome> {
  const refreshToken = getRefreshToken();
  if (!refreshToken) {
    escalate("no-refresh-token");
    return { accessToken: null, failureReason: "no-refresh-token" };
  }

  try {
    const response = await fetch("/api/v1/auth/refresh", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refreshToken }),
    });

    if (!response.ok) {
      escalate("rejected");
      return { accessToken: null, failureReason: "rejected" };
    }

    const data = (await response.json()) as RefreshResponse;
    // The refresh token itself never rotates (see spec D1/D2) — only the
    // access token and its expiry change, so the stored refresh token is
    // left untouched.
    setSession(data.accessToken, undefined, data.expiresIn);
    return { accessToken: data.accessToken };
  } catch {
    // Network error: leave the session as-is (don't clear a possibly-valid
    // refresh token over a transient blip) and report failure for this call.
    logRefreshFailure("network-error");
    return { accessToken: null, failureReason: "network-error" };
  }
}

/**
 * Refreshes the access token using the stored refresh token, and returns
 * *why* it failed when it does — callers that need to decide whether to
 * keep retrying (network blip) or give up (no refresh token / rejected)
 * should use this instead of `refreshAccessToken`. Concurrent callers share
 * the same in-flight request; the shared promise resets once it settles so
 * the *next* call starts a fresh attempt.
 */
export function refreshWithOutcome(): Promise<RefreshOutcome> {
  if (!inFlight) {
    inFlight = doRefresh().finally(() => {
      inFlight = null;
    });
  }
  return inFlight;
}

/**
 * Refreshes the access token using the stored refresh token. Resolves the
 * new access token, or null when there is no refresh token to use or the
 * refresh itself failed (for any reason — see `refreshWithOutcome` for the
 * differentiated reason). A failure that means the session is definitively
 * over ("no-refresh-token" / "rejected") has already cleared the session
 * and redirected to login by the time this resolves.
 */
export function refreshAccessToken(): Promise<string | null> {
  return refreshWithOutcome().then((outcome) => outcome.accessToken);
}

/**
 * Proactive-refresh scheduling decision, extracted as a pure function so it
 * can be unit tested without mounting AuthProvider (this codebase has no
 * jsdom/testing-library setup — see live-socket.ts's exported backoffDelay
 * for the same pattern). Refreshes once less than 1/3 of the access token's
 * original lifetime remains until `expiresAt`. Both `expiresAt` and
 * `expiresInSeconds` are null together (a session predating this field, or
 * no session at all) — in that case there's nothing to schedule against.
 */
export function shouldRefreshNow(
  expiresAt: number | null,
  expiresInSeconds: number | null,
  now: number = Date.now()
): boolean {
  if (expiresAt === null || expiresInSeconds === null) return false;

  const remainingMs = expiresAt - now;
  const thresholdMs = (expiresInSeconds * 1000) / 3;
  return remainingMs < thresholdMs;
}
