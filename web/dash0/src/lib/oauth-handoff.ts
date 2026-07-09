// Persists an external OAuth provider's redirect (GitHub/Google/Slack — see
// server/internal/handlers/auth/{github,google,slack}.go buildSuccessRedirect)
// into the same full-session storage every other login-shaped path uses.
//
// Extracted out of main.tsx's pre-React IIFE so the parsing/persistence
// logic is unit-testable in isolation: main.tsx has no test harness of its
// own (it wires up the router, React root, and service worker at module
// load, touching window/DOM as a side effect of being imported).
//
// This used to call `setToken(accessToken)` only, dropping `refresh_token`
// and `expires_in` even though the backend redirect carries both — the
// funnel-audit bug behind the 2026-07-08 zombie-socket incident (see the
// spec: a session with an access token but no refresh token silently
// disables every refresh layer). `$org.tsx`'s OrgLayout has a second,
// equivalent handoff effect that reads the same query params — it never
// actually fires for a real browser redirect because this IIFE runs first
// and strips them via history.replaceState before React mounts, but it's
// left in place as harmless defense-in-depth.

import { setSession } from "@/api/client";

export interface OAuthHandoff {
  accessToken: string;
  refreshToken?: string;
  expiresIn?: number;
  org?: string;
}

/**
 * Parses the OAuth redirect's query string. Returns null when there's
 * nothing to hand off (no `access_token` param — the common case, every
 * normal page load).
 */
export function parseOAuthHandoff(search: string): OAuthHandoff | null {
  const params = new URLSearchParams(search);
  const accessToken = params.get("access_token");
  if (!accessToken) return null;

  const refreshToken = params.get("refresh_token") || undefined;
  const expiresInRaw = params.get("expires_in");
  const expiresInParsed = expiresInRaw ? parseInt(expiresInRaw, 10) : NaN;
  const expiresIn = isNaN(expiresInParsed) ? undefined : expiresInParsed;
  const org = params.get("org") || undefined;

  return { accessToken, refreshToken, expiresIn, org };
}

/**
 * Persists the full session (access + refresh + expiry) from an OAuth
 * redirect, and remembers the org so the caller can navigate there. Must
 * run before any authenticated API call fires, or org-scoped queries 401
 * before the token lands — see main.tsx's call site, which does this
 * synchronously at module load, before the router or React Query mount.
 */
export function applyOAuthHandoff(handoff: OAuthHandoff): void {
  setSession(handoff.accessToken, handoff.refreshToken, handoff.expiresIn);
  if (handoff.org) {
    localStorage.setItem("solidping_org", handoff.org);
  }
}
