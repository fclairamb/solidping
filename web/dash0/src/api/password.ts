import { apiFetch } from "./client";

export interface ChangePasswordResponse {
  message: string;
}

/**
 * Rotates the signed-in user's password, or sets an initial one when the
 * account has none yet (SSO-only sign-up — `hasPassword: false` on
 * /api/v1/auth/me). `currentPassword` is ignored in that case.
 *
 * `suppress401Handling` is required here: the endpoint answers 401 with
 * INVALID_CURRENT_PASSWORD when the *body* credential is wrong, which has
 * nothing to do with the bearer token. Without it, apiFetch's session-expiry
 * path would clear the session and bounce the user to the login screen for a
 * simple typo.
 */
export async function changePassword(
  newPassword: string,
  currentPassword?: string
): Promise<ChangePasswordResponse> {
  return apiFetch<ChangePasswordResponse>("/api/v1/auth/change-password", {
    method: "POST",
    body: JSON.stringify({ currentPassword: currentPassword ?? "", newPassword }),
    suppress401Handling: true,
  });
}
