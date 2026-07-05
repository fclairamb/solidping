// Deliberately a static import despite the module cycle (token-refresh.ts
// imports getRefreshToken/setSession/clearToken back from this file): both
// sides only reference the imported bindings inside function bodies, never
// at module-evaluation time, so ESM's circular-import handling resolves it
// safely. Keeping it static (vs. a dynamic import()) keeps the single-flight
// behavior synchronous-enough to unit test without extra await hops.
import { refreshAccessToken } from "@/lib/token-refresh";

const TOKEN_KEY = "solidping_session_token";
const REFRESH_TOKEN_KEY = "solidping_refresh_token";
const EXPIRES_AT_KEY = "solidping_expires_at";

// Timestamp of the most recent apiFetch activity (request start or
// completion). The live-socket connection waits for a quiet gap before opening
// its long-lived connection so it never competes with first-paint data
// fetching — and so load-completion heuristics (e.g. Playwright's
// `networkidle`, which needs 500ms with zero in-flight requests) can latch
// before a permanent connection appears.
let lastApiActivityTs = 0;

export function noteApiActivity(): void {
  lastApiActivityTs = Date.now();
}

export function msSinceLastApiActivity(): number {
  if (lastApiActivityTs === 0) return Number.MAX_SAFE_INTEGER;
  return Date.now() - lastApiActivityTs;
}

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}

export function setToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token);
}

export function getRefreshToken(): string | null {
  return localStorage.getItem(REFRESH_TOKEN_KEY);
}

/** Epoch-ms deadline for the current access token, or null if unknown
 * (e.g. a session predating this field). */
export function getExpiresAt(): number | null {
  const raw = localStorage.getItem(EXPIRES_AT_KEY);
  if (!raw) return null;
  const parsed = parseInt(raw, 10);
  return isNaN(parsed) ? null : parsed;
}

/** Seconds remaining in the access token's lifetime at issuance, stored
 * verbatim so the proactive-refresh scheduler can derive its "1/3 of
 * lifetime remaining" threshold without reconstructing issuedAt. */
export function getExpiresInSeconds(): number | null {
  const raw = localStorage.getItem("solidping_expires_in");
  if (!raw) return null;
  const parsed = parseInt(raw, 10);
  return isNaN(parsed) ? null : parsed;
}

/**
 * Persists the whole session — access token, refresh token, and the
 * computed absolute expiry — in one call. Every login-shaped response
 * (password, 2FA, passkey, OAuth, Slack, switch-org, refresh) must funnel
 * through here instead of calling setToken alone, or the refresh token and
 * expiry tracking silently go missing for that path.
 */
export function setSession(
  accessToken: string,
  refreshToken?: string,
  expiresIn?: number
): void {
  localStorage.setItem(TOKEN_KEY, accessToken);
  if (refreshToken) {
    localStorage.setItem(REFRESH_TOKEN_KEY, refreshToken);
  }
  if (typeof expiresIn === "number" && !isNaN(expiresIn)) {
    localStorage.setItem(EXPIRES_AT_KEY, String(Date.now() + expiresIn * 1000));
    localStorage.setItem("solidping_expires_in", String(expiresIn));
  }
}

export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY);
  localStorage.removeItem(REFRESH_TOKEN_KEY);
  localStorage.removeItem(EXPIRES_AT_KEY);
  localStorage.removeItem("solidping_expires_in");
}

interface FetchOptions extends RequestInit {
  skipAuth?: boolean;
  suppress401Redirect?: boolean;
  /** Internal: set on the single retry attempt after a refresh-and-retry, so
   * a second 401 clears/redirects instead of looping back into another
   * refresh. Callers should never set this themselves. */
  _isRetry?: boolean;
}

export class NetworkError extends Error {
  constructor(message: string = "Network connection failed") {
    super(message);
    this.name = "NetworkError";
  }
}

export class ApiError extends Error {
  constructor(
    message: string,
    public code: string,
    public detail?: string,
    public status?: number,
    public retryAfter?: number
  ) {
    super(message);
    this.name = "ApiError";
  }
}

function getStoredOrg(): string | null {
  return localStorage.getItem("solidping_org");
}

function extractOrgFromPath(path: string): string | null {
  const match = path.match(/(?:^|\/)orgs\/([^/]+)/);
  return match ? match[1] : null;
}

function redirectToExpiredLogin(): void {
  const currentPath = window.location.pathname;
  if (!currentPath.endsWith("/login")) {
    const basepath = import.meta.env.VITE_BASE_URL || "";
    const org = extractOrgFromPath(currentPath) || getStoredOrg() || "default";
    const returnTo = currentPath + window.location.search;
    window.location.href = `${basepath}/orgs/${org}/login?session_expired=true&returnTo=${encodeURIComponent(returnTo)}`;
  }
}

async function doFetch(url: string, headers: Headers, fetchOptions: RequestInit): Promise<Response> {
  noteApiActivity();
  try {
    const response = await fetch(url, { ...fetchOptions, headers });
    noteApiActivity();
    return response;
  } catch {
    noteApiActivity();
    throw new NetworkError();
  }
}

export async function apiFetch<T>(
  url: string,
  options: FetchOptions = {}
): Promise<T> {
  const { skipAuth = false, suppress401Redirect = false, ...fetchOptions } = options;

  const headers = new Headers(fetchOptions.headers);

  if (!skipAuth) {
    const token = getToken();
    if (token) {
      headers.set("Authorization", `Bearer ${token}`);
    }
  }

  if (
    fetchOptions.body &&
    typeof fetchOptions.body === "string" &&
    !headers.has("Content-Type")
  ) {
    headers.set("Content-Type", "application/json");
  }

  let response = await doFetch(url, headers, fetchOptions);

  // Reactive refresh: a 401 first tries one silent refresh-and-retry before
  // falling through to today's clear+redirect. `_isRetry` guards against a
  // retried request itself 401ing (e.g. the refresh succeeded but the new
  // token is somehow still rejected, or the refresh raced a logout) —
  // without it a second 401 would try to refresh again and loop.
  if (response.status === 401 && !skipAuth && !options._isRetry) {
    const newToken = await refreshAccessToken();

    if (newToken) {
      const retryHeaders = new Headers(headers);
      retryHeaders.set("Authorization", `Bearer ${newToken}`);
      response = await doFetch(url, retryHeaders, fetchOptions);
      // Fall through to the normal status handling below with the retried
      // response. A second 401 here is not retried again — handleResponse
      // just clears/redirects, since the caller of doFetch above never
      // re-enters this refresh branch.
    }
  }

  return handleResponse<T>(response, { skipAuth, suppress401Redirect });
}

async function handleResponse<T>(
  response: Response,
  { skipAuth, suppress401Redirect }: { skipAuth: boolean; suppress401Redirect: boolean }
): Promise<T> {
  if (response.status === 401 && !skipAuth) {
    clearToken();
    if (!suppress401Redirect) {
      redirectToExpiredLogin();
    }
    throw new ApiError("Session expired", "UNAUTHORIZED", undefined, 401);
  }

  if (response.status === 429) {
    const retryAfterHeader = response.headers.get("Retry-After");
    const retryAfter = retryAfterHeader ? parseInt(retryAfterHeader, 10) : undefined;
    const error = await response.json().catch(() => ({}));
    throw new ApiError(
      error.title || "Too many requests",
      error.code || "RATE_LIMITED",
      error.detail,
      429,
      retryAfter && !isNaN(retryAfter) ? retryAfter : undefined
    );
  }

  if (!response.ok) {
    const error = await response.json().catch(() => ({}));
    throw new ApiError(
      error.title || "An error occurred",
      error.code || "UNKNOWN_ERROR",
      error.detail,
      response.status
    );
  }

  if (response.status === 204) {
    return undefined as T;
  }

  return response.json();
}
