import { apiFetch } from "./client";

// TOTP setup: returns the QR-code-friendly otpauth URI plus the manual
// secret string. The user scans either, types the 6-digit code into
// confirm, and we hand back recovery codes.
export interface Setup2FAResponse {
  secret: string;
  uri: string;
  // Proof of this setup: confirm2FA must send it back (spec 2026-10-31-02).
  setupToken: string;
}

export interface Confirm2FAResponse {
  recoveryCodes: string[];
}

// VerifyResponse mirrors LoginResponse — same shape returned on
// /auth/login when 2FA isn't required. The dash auth context already
// knows how to consume this.
export interface Verify2FAResponse {
  accessToken: string;
  refreshToken?: string;
  expiresIn: number;
  tokenType: string;
  user: { email: string; name?: string; avatarUrl?: string; role: string };
  organization?: { uid: string; slug: string; name?: string };
  organizations: Array<{ slug: string; name?: string; role: string }>;
  loginAction?: string;
}

// Adding a sign-in factor needs a re-authentication (spec 2026-10-31-02):
// the current password when the account has one. Without one, the session
// must come from a sign-in within the last 10 minutes, else the server
// answers 403 REAUTHENTICATION_REQUIRED. A wrong password answers 401
// INVALID_CURRENT_PASSWORD, about the body and not the session, hence
// suppress401Handling.
export async function setup2FA(currentPassword?: string): Promise<Setup2FAResponse> {
  return apiFetch<Setup2FAResponse>("/api/v1/auth/2fa/setup", {
    method: "POST",
    body: JSON.stringify(currentPassword !== undefined ? { currentPassword } : {}),
    suppress401Handling: true,
  });
}

// A wrong code answers 401 INVALID_2FA_CODE, about the body as well.
export async function confirm2FA(
  code: string,
  setupToken: string,
): Promise<Confirm2FAResponse> {
  return apiFetch<Confirm2FAResponse>("/api/v1/auth/2fa/confirm", {
    method: "POST",
    body: JSON.stringify({ code, setupToken }),
    suppress401Handling: true,
  });
}

// The temp token from the 2FA login challenge travels as a Bearer header —
// the backend never reads it from the body.
export async function verify2FA(
  tempToken: string,
  code: string,
): Promise<Verify2FAResponse> {
  return apiFetch<Verify2FAResponse>("/api/v1/auth/2fa/verify", {
    method: "POST",
    headers: { Authorization: `Bearer ${tempToken}` },
    body: JSON.stringify({ code }),
    skipAuth: true,
  });
}

export async function recovery2FA(
  tempToken: string,
  recoveryCode: string,
): Promise<Verify2FAResponse> {
  return apiFetch<Verify2FAResponse>("/api/v1/auth/2fa/recovery", {
    method: "POST",
    headers: { Authorization: `Bearer ${tempToken}` },
    body: JSON.stringify({ recoveryCode }),
    skipAuth: true,
  });
}

// A wrong code answers 401 INVALID_2FA_CODE: that is about the code in the
// body, not the session. Without suppress401Handling the client would
// refresh-and-retry (spending two attempts of the per-user 2FA budget per
// click) and then sign the user out.
export async function disable2FA(code: string): Promise<void> {
  await apiFetch("/api/v1/auth/2fa", {
    method: "DELETE",
    body: JSON.stringify({ code }),
    suppress401Handling: true,
  });
}
