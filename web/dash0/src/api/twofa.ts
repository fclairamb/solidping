import { apiFetch } from "./client";

// TOTP setup: returns the QR-code-friendly otpauth URI plus the manual
// secret string. The user scans either, types the 6-digit code into
// confirm, and we hand back recovery codes.
export interface Setup2FAResponse {
  secret: string;
  uri: string;
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

export async function setup2FA(): Promise<Setup2FAResponse> {
  return apiFetch<Setup2FAResponse>("/api/v1/auth/2fa/setup", { method: "POST" });
}

export async function confirm2FA(code: string): Promise<Confirm2FAResponse> {
  return apiFetch<Confirm2FAResponse>("/api/v1/auth/2fa/confirm", {
    method: "POST",
    body: JSON.stringify({ code }),
  });
}

export async function verify2FA(
  tempToken: string,
  code: string,
): Promise<Verify2FAResponse> {
  return apiFetch<Verify2FAResponse>("/api/v1/auth/2fa/verify", {
    method: "POST",
    body: JSON.stringify({ tempToken, code }),
    skipAuth: true,
  });
}

export async function recovery2FA(
  tempToken: string,
  recoveryCode: string,
): Promise<Verify2FAResponse> {
  return apiFetch<Verify2FAResponse>("/api/v1/auth/2fa/recovery", {
    method: "POST",
    body: JSON.stringify({ tempToken, recoveryCode }),
    skipAuth: true,
  });
}

export async function disable2FA(code: string): Promise<void> {
  await apiFetch("/api/v1/auth/2fa", {
    method: "DELETE",
    body: JSON.stringify({ code }),
  });
}
