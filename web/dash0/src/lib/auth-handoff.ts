// Completes a federated login (Google, GitHub, GitLab, Microsoft, Discord,
// Slack, OIDC, SAML, the Slack app install) — spec 2026-09-25-12.
//
// The provider callback no longer puts the session tokens in the redirect URL.
// It stores the session server-side under a single-use, 60-second handoff
// code and redirects to `/d/auth/complete?code=…` (plus `membershipPending`
// when the org did not admit the user). The `auth.complete` route trades the
// code once through POST /api/v1/auth/handoff/exchange, feeds the
// login-shaped answer to AuthContext's applyLoginResponse, then navigates to
// what resolveHandoffLanding picks.
//
// The code param is deliberately named `code`: lib/analytics-redaction.ts
// scrubs it from every URL PostHog sees.

import { apiFetch } from "@/api/client";
import { resolveDestination } from "./login-destination";

/** The route every provider callback redirects to (see join_policy.go). */
export const HANDOFF_COMPLETE_PATH = "/auth/complete";

/**
 * POST /api/v1/auth/handoff/exchange. The login response shape (the same one
 * `applyLoginResponse` consumes for a password login) plus where to land.
 */
export interface HandoffExchangeResponse {
  accessToken: string;
  refreshToken?: string;
  expiresIn?: number;
  user: {
    uid: string;
    email: string;
    name?: string;
    avatarUrl?: string;
    role: string;
    mustChangePassword?: boolean;
    demo?: boolean;
  };
  organization?: { uid: string; slug: string; name?: string };
  organizations?: { slug: string; name?: string; logoUrl?: string | null; role: string }[];
  loginAction?: string;
  /** The path the login started from. Followed only through resolveDestination's guards. */
  returnTo?: string;
  /** The org that has not admitted the user yet, for /no-org. */
  membershipPending?: string;
}

/** Where the browser goes once the exchanged session is stored. */
export type HandoffLanding =
  | { href: string }
  | { to: "/orgs/$org"; params: { org: string } }
  | { to: "/no-org"; search: { membershipPending?: string } };

/**
 * Picks the landing for an exchanged session. Pure: no router, no DOM.
 *
 * - An org-scoped session goes through {@link resolveDestination}, the funnel
 *   every other login uses: the `returnTo` is kept only when it is a safe,
 *   same-origin, in-app path in the session's org (or the MCP authorize /
 *   device-verification bounce); anything else lands on the org root. This is
 *   what lets a deep link or an MCP consent flow survive the provider
 *   round-trip, and what keeps a forged `returnTo` from going anywhere.
 * - An org-less session (the org did not admit the user) lands on /no-org,
 *   naming the org that has the pending request. The exchange's answer wins
 *   over the URL's flag; the URL's is the fallback.
 */
export function resolveHandoffLanding(
  response: Pick<HandoffExchangeResponse, "organization" | "returnTo" | "membershipPending">,
  membershipPendingFromUrl: string | undefined,
  basepath: string,
): HandoffLanding {
  const org = response.organization?.slug;
  if (org) {
    return resolveDestination(org, response.returnTo ?? null, basepath);
  }

  const membershipPending = response.membershipPending || membershipPendingFromUrl || undefined;
  return { to: "/no-org", search: { membershipPending } };
}

/**
 * Exchanges in flight, keyed by code. The code is single use, so a second
 * POST for the same code (React StrictMode runs effects twice in development)
 * would get a 401 and undo a perfectly good sign-in. Every caller for one code
 * shares one request instead.
 */
const inflight = new Map<string, Promise<HandoffExchangeResponse>>();

/** Redeems a handoff code. Rejects with an ApiError (401) when it is unusable. */
export function exchangeHandoffCode(code: string): Promise<HandoffExchangeResponse> {
  let pending = inflight.get(code);
  if (!pending) {
    pending = apiFetch<HandoffExchangeResponse>("/api/v1/auth/handoff/exchange", {
      method: "POST",
      body: JSON.stringify({ code }),
      skipAuth: true,
    });
    inflight.set(code, pending);
  }
  return pending;
}
