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
