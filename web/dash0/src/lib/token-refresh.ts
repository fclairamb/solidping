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

import { clearToken, getRefreshToken, setSession } from "@/api/client";

interface RefreshResponse {
  accessToken: string;
  expiresIn?: number;
}

let inFlight: Promise<string | null> | null = null;

async function doRefresh(): Promise<string | null> {
  const refreshToken = getRefreshToken();
  if (!refreshToken) {
    return null;
  }

  try {
    const response = await fetch("/api/v1/auth/refresh", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refreshToken }),
    });

    if (!response.ok) {
      clearToken();
      return null;
    }

    const data = (await response.json()) as RefreshResponse;
    // The refresh token itself never rotates (see spec D1/D2) — only the
    // access token and its expiry change, so the stored refresh token is
    // left untouched.
    setSession(data.accessToken, undefined, data.expiresIn);
    return data.accessToken;
  } catch {
    // Network error: leave the session as-is (don't clear a possibly-valid
    // refresh token over a transient blip) and report failure for this call.
    return null;
  }
}

/**
 * Refreshes the access token using the stored refresh token. Concurrent
 * callers share the same in-flight request; the shared promise resets to
 * null once it settles (success or failure) so the *next* call starts a
 * fresh attempt. Resolves the new access token, or null when there is no
 * refresh token to use or the refresh itself failed.
 */
export function refreshAccessToken(): Promise<string | null> {
  if (!inFlight) {
    inFlight = doRefresh().finally(() => {
      inFlight = null;
    });
  }
  return inFlight;
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
