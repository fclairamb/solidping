import { apiFetch } from "./client";

// Wire types matching the Go handlers in
// server/internal/handlers/auth/passkey_*.go. The credential field is
// passed through as the raw PublicKeyCredentialJSON returned by
// navigator.credentials.create / get — @simplewebauthn/browser handles
// the base64url ↔ ArrayBuffer plumbing.

export interface PasskeyInfo {
  uid: string;
  name: string;
  aaguidLabel?: string;
  transports?: string[];
  backupState?: boolean;
  createdAt: string;
  lastUsedAt?: string;
}

export interface PasskeyListResponse {
  data: PasskeyInfo[];
}

// Begin / Finish wire shapes use generic objects for the WebAuthn options
// (the library's TypeScript types live in @simplewebauthn/browser).
//
// We keep the credential payload as `unknown` to avoid pulling the
// library's full type tree into every consumer.
export interface RegisterBeginResponse {
  options: { publicKey: unknown };
  session: string;
}

export interface RegisterFinishResponse {
  passkey: PasskeyInfo;
}

export interface LoginBeginResponse {
  options: { publicKey: unknown };
  session: string;
}

export interface PasskeyLoginResponse {
  accessToken: string;
  refreshToken?: string;
  expiresIn: number;
  tokenType: string;
  user: { email: string; name?: string; avatarUrl?: string; role: string };
  organization?: { uid: string; slug: string; name?: string };
  organizations: Array<{ slug: string; name?: string; role: string }>;
  loginAction?: string;
}

export async function beginPasskeyRegistration(): Promise<RegisterBeginResponse> {
  return apiFetch<RegisterBeginResponse>("/api/v1/auth/passkeys/register/begin", {
    method: "POST",
  });
}

export async function finishPasskeyRegistration(
  session: string,
  credential: unknown,
  name: string,
): Promise<RegisterFinishResponse> {
  return apiFetch<RegisterFinishResponse>("/api/v1/auth/passkeys/register/finish", {
    method: "POST",
    body: JSON.stringify({ session, credential, name }),
  });
}

export async function beginPasskeyLogin(email?: string): Promise<LoginBeginResponse> {
  return apiFetch<LoginBeginResponse>("/api/v1/auth/passkeys/login/begin", {
    method: "POST",
    body: JSON.stringify(email ? { email } : {}),
    skipAuth: true,
  });
}

export async function finishPasskeyLogin(
  session: string,
  credential: unknown,
  org?: string,
): Promise<PasskeyLoginResponse> {
  return apiFetch<PasskeyLoginResponse>("/api/v1/auth/passkeys/login/finish", {
    method: "POST",
    body: JSON.stringify({ session, credential, org: org ?? "" }),
    skipAuth: true,
  });
}

export async function listPasskeys(): Promise<PasskeyListResponse> {
  return apiFetch<PasskeyListResponse>("/api/v1/auth/passkeys");
}

export async function renamePasskey(uid: string, name: string): Promise<void> {
  await apiFetch(`/api/v1/auth/passkeys/${uid}`, {
    method: "PATCH",
    body: JSON.stringify({ name }),
  });
}

export async function deletePasskey(uid: string): Promise<void> {
  await apiFetch(`/api/v1/auth/passkeys/${uid}`, {
    method: "DELETE",
  });
}

// AuthProviders extends the existing /auth/providers shape with
// passkeysEnabled. Lives here for now since both flags drive the login
// page's passkey rendering.
export interface AuthProvidersResponse {
  data: Array<{ name: string; type: string }>;
  registrationEnabled: boolean;
  passkeysEnabled: boolean;
}

export async function getAuthProviders(): Promise<AuthProvidersResponse> {
  return apiFetch<AuthProvidersResponse>("/api/v1/auth/providers", {
    skipAuth: true,
  });
}
