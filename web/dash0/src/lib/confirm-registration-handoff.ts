import { setSession } from "@/api/client";

export interface ConfirmRegistrationResult {
  accessToken: string;
  refreshToken?: string;
  expiresIn?: number;
  organization?: { uid: string; slug: string; name?: string };
}

export type ConfirmRegistrationNavTarget =
  | { to: "/orgs/$org"; params: { org: string } }
  | { to: "/no-org" };

/**
 * Applies a confirm-registration success response: persists the full
 * session (access + refresh token + expiry) and returns where to navigate
 * next.
 *
 * This mirrors applyOAuthHandoff's fix — confirm-registration is a
 * login-shaped response (the backend mints a full session, refresh token
 * included, exactly like password/OAuth login) so it must funnel through
 * `setSession` with all three arguments. The route used to call
 * `setSession(data.accessToken, data.refreshToken, data.expiresIn)` only
 * after the 2026-07-08 funnel audit fixed a since-removed `setToken` call
 * that silently dropped the refresh token and expiry (see api/client.ts's
 * `setSession` doc comment). This function exists so that regression has a
 * unit test without needing to render the route component.
 */
export function applyConfirmRegistrationHandoff(
  data: ConfirmRegistrationResult
): ConfirmRegistrationNavTarget {
  setSession(data.accessToken, data.refreshToken, data.expiresIn);

  const orgSlug = data.organization?.slug;
  if (orgSlug) {
    return { to: "/orgs/$org", params: { org: orgSlug } };
  }
  return { to: "/no-org" };
}
