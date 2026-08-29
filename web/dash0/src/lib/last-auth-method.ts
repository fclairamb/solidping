// Remembers the authentication method a returning user picked last so the
// login page can promote it to the top with a "Last used" badge. A single
// global per-browser value (no org in the key). Stored values:
//   - OAuth provider → "oauth:<type>" (e.g. "oauth:google")
//   - Passkey        → "passkey"
//   - Email+password → "password"
//
// Guards mirror the existing storage helpers (getToken/setSession in
// api/client.ts, the collapsed-groups map in routes/orgs/$org/checks.index.tsx):
// SSR-safe via a typeof-window check, and every access is wrapped in try/catch
// because localStorage can throw in private mode or when storage is disabled.

export const LAST_AUTH_METHOD_KEY = "solidping_last_auth_method";

export function getLastAuthMethod(): string | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage.getItem(LAST_AUTH_METHOD_KEY);
  } catch {
    // Storage may be unavailable (private mode / disabled) — degrade silently.
    return null;
  }
}

export function setLastAuthMethod(method: string): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(LAST_AUTH_METHOD_KEY, method);
  } catch {
    // Storage may be unavailable (private mode / disabled) — the login flow
    // still works, we just won't remember the choice next time.
  }
}
