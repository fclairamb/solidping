import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// token-refresh has no DOM dependency of its own except localStorage (via
// api/client's getters/setters) — vitest.config.ts runs tests in node env,
// and Node's built-in localStorage stub throws without a backing file, so
// (matching live-socket.test.ts's established pattern in this repo) the
// storage layer is mocked here instead of touched for real.
let storedRefreshToken: string | null = null;
const setSessionMock = vi.fn((_accessToken: string, refreshToken?: string) => {
  if (refreshToken) storedRefreshToken = refreshToken;
});
const clearTokenMock = vi.fn(() => {
  storedRefreshToken = null;
});

vi.mock("@/api/client", () => ({
  getRefreshToken: () => storedRefreshToken,
  setSession: (accessToken: string, refreshToken?: string, expiresIn?: number) =>
    setSessionMock(accessToken, refreshToken, expiresIn),
  clearToken: () => clearTokenMock(),
}));

import { refreshAccessToken } from "./token-refresh";

function mockFetchOnce(response: { ok: boolean; status?: number; body?: unknown }) {
  return vi.fn().mockResolvedValueOnce({
    ok: response.ok,
    status: response.status ?? (response.ok ? 200 : 401),
    json: async () => response.body ?? {},
  });
}

describe("refreshAccessToken", () => {
  beforeEach(() => {
    storedRefreshToken = "stored-refresh-token";
    setSessionMock.mockClear();
    clearTokenMock.mockClear();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("resolves null without a network call when there is no stored refresh token", async () => {
    storedRefreshToken = null;
    const fetchSpy = vi.fn();
    vi.stubGlobal("fetch", fetchSpy);

    const result = await refreshAccessToken();

    expect(result).toBeNull();
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("posts to /api/v1/auth/refresh and persists the new access token + expiry", async () => {
    const fetchSpy = mockFetchOnce({
      ok: true,
      body: { accessToken: "new-access", expiresIn: 3600 },
    });
    vi.stubGlobal("fetch", fetchSpy);

    const result = await refreshAccessToken();

    expect(result).toBe("new-access");
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(fetchSpy).toHaveBeenCalledWith(
      "/api/v1/auth/refresh",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ refreshToken: "stored-refresh-token" }),
      })
    );
    // The refresh token itself never rotates — setSession is called with no
    // second argument, leaving the previously stored refresh token in place.
    expect(setSessionMock).toHaveBeenCalledWith("new-access", undefined, 3600);
    expect(clearTokenMock).not.toHaveBeenCalled();
  });

  it("clears the session and resolves null when the server rejects the refresh token", async () => {
    const fetchSpy = mockFetchOnce({ ok: false, status: 401 });
    vi.stubGlobal("fetch", fetchSpy);

    const result = await refreshAccessToken();

    expect(result).toBeNull();
    expect(clearTokenMock).toHaveBeenCalledTimes(1);
  });

  it("resolves null and leaves the session untouched on a network error", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValueOnce(new Error("network down")));

    const result = await refreshAccessToken();

    expect(result).toBeNull();
    // Unlike a rejected refresh token, a transient network error must not
    // wipe a possibly-still-valid refresh token — the next attempt (timer
    // tick, or another 401) should retry rather than being permanently
    // logged out by one blip.
    expect(clearTokenMock).not.toHaveBeenCalled();
  });

  it("is single-flight: N concurrent callers share one in-flight request", async () => {
    let resolveFetch: (value: unknown) => void = () => {};
    const fetchSpy = vi.fn().mockReturnValueOnce(
      new Promise((resolve) => {
        resolveFetch = resolve;
      })
    );
    vi.stubGlobal("fetch", fetchSpy);

    const calls = [refreshAccessToken(), refreshAccessToken(), refreshAccessToken()];
    expect(fetchSpy).toHaveBeenCalledTimes(1);

    resolveFetch({
      ok: true,
      status: 200,
      json: async () => ({ accessToken: "shared-access", expiresIn: 3600 }),
    });

    const results = await Promise.all(calls);
    expect(results).toEqual(["shared-access", "shared-access", "shared-access"]);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
  });

  it("starts a fresh attempt for the next caller after the in-flight one settles", async () => {
    const firstFetch = mockFetchOnce({
      ok: true,
      body: { accessToken: "first-access", expiresIn: 3600 },
    });
    vi.stubGlobal("fetch", firstFetch);
    await refreshAccessToken();

    const secondFetch = mockFetchOnce({
      ok: true,
      body: { accessToken: "second-access", expiresIn: 3600 },
    });
    vi.stubGlobal("fetch", secondFetch);
    const second = await refreshAccessToken();

    expect(second).toBe("second-access");
    expect(secondFetch).toHaveBeenCalledTimes(1);
  });
});
