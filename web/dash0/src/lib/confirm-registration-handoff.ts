import { setSession } from "@/api/client";

export interface ConfirmRegistrationResult {
  // Optional, not `string`: the zero-org branch used to omit this entirely
  // (spec 2026-08-29-06 — fixed server-side, but the frontend must not
  // trust that forever). A response with no accessToken is a failure path,
  // never a session to persist.
  accessToken?: string;
  refreshToken?: string;
  expiresIn?: number;
  organization?: { uid: string; slug: string; name?: string };
}

export type ConfirmRegistrationNavTarget =
  | { to: "/orgs/$org"; params: { org: string } }
  | { to: "/no-org" };

/**
 * Thrown by {@link applyConfirmRegistrationHandoff} when the confirmation
 * response carries no access token. The account was still created — the
 * caller should tell the user to log in, not that confirmation failed (spec
 * 2026-08-29-06: re-registering after a false "failed" reading fails again
 * with email-taken).
 */
export class ConfirmRegistrationSessionError extends Error {
  constructor() {
    super("Registration confirmed, but no session was returned");
    this.name = "ConfirmRegistrationSessionError";
  }
}

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
 *
 * A response with no `accessToken` (spec 2026-08-29-06) is never forwarded
 * to `setSession` — that used to persist the literal string "undefined"
 * and get the freshly-created user instantly bounced back to login. It is
 * treated as an error instead, via {@link ConfirmRegistrationSessionError}.
 */
export function applyConfirmRegistrationHandoff(
  data: ConfirmRegistrationResult
): ConfirmRegistrationNavTarget {
  if (!data.accessToken) {
    throw new ConfirmRegistrationSessionError();
  }

  setSession(data.accessToken, data.refreshToken, data.expiresIn);

  const orgSlug = data.organization?.slug;
  if (orgSlug) {
    return { to: "/orgs/$org", params: { org: orgSlug } };
  }
  return { to: "/no-org" };
}
