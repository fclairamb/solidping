export class ApiError extends Error {
  constructor(
    message: string,
    public code: string,
    public detail?: string,
    public status?: number
  ) {
    super(message);
    this.name = "ApiError";
  }
}

export class NetworkError extends Error {
  constructor(message: string = "Network connection failed") {
    super(message);
    this.name = "NetworkError";
  }
}

export async function apiFetch<T>(url: string): Promise<T> {
  let response: Response;
  try {
    // "no-cache": always ask the server. Public status-page responses carry
    // `public, max-age=60, stale-while-revalidate=30` so a CDN can absorb an
    // incident spike, but the browser obeys it too: a page polling every 30 s
    // was answered from its own disk cache and showed data up to 90 s old.
    // The CDN in front still caches; the server's 15 s memo absorbs the rest.
    response = await fetch(url, { cache: "no-cache" });
  } catch {
    throw new NetworkError();
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

  return response.json();
}
