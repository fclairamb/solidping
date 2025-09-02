const TOKEN_KEY = "solidping_session_token";

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}

export function setToken(token: string): void {
  localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY);
}

interface FetchOptions extends RequestInit {
  skipAuth?: boolean;
  suppress401Redirect?: boolean;
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

  let response: Response;
  try {
    response = await fetch(url, {
      ...fetchOptions,
      headers,
    });
  } catch {
    throw new NetworkError();
  }

  if (response.status === 401 && !skipAuth) {
    clearToken();
    if (!suppress401Redirect) {
      const currentPath = window.location.pathname;
      if (!currentPath.endsWith("/login")) {
        const basepath = import.meta.env.VITE_BASE_URL || "";
        const org = extractOrgFromPath(currentPath) || getStoredOrg() || "default";
        const returnTo = currentPath + window.location.search;
        window.location.href = `${basepath}/orgs/${org}/login?session_expired=true&returnTo=${encodeURIComponent(returnTo)}`;
      }
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
