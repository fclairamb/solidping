import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  ApiError,
  apiFetch,
  getRefreshToken,
  getToken,
  handleResponse,
  setLoggingOut,
  setSession,
} from "./client";
import i18n from "@/i18n";
import frOrg from "@/locales/fr/org.json";
import enOrg from "@/locales/en/org.json";
import { toast } from "sonner";
import { DASH_BASE } from "@/lib/base-path";

vi.mock("sonner", () => ({
  toast: { info: vi.fn(), error: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));

// Regression test for spec 2026-08-29-06: a login-shaped response missing
// its access token (the confirm-registration zero-org bug) used to reach
// here and get `localStorage.setItem(TOKEN_KEY, undefined)`, which
// JavaScript coerces to the literal string "undefined" — a session that
// LOOKS present but sends `Authorization: Bearer undefined` on every
// request and gets the user logged straight back out.
describe("setSession", () => {
  let store: Record<string, string>;

  beforeEach(() => {
    store = {};
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store[k] ?? null,
      setItem: (k: string, v: string) => {
        store[k] = v;
      },
      removeItem: (k: string) => {
        delete store[k];
      },
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("persists a real access token normally", () => {
    setSession("at-1", "rt-1", 3600);

    expect(getToken()).toBe("at-1");
  });

  it("refuses to persist a falsy access token instead of writing the string \"undefined\"", () => {
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});

    // @ts-expect-error — exercising the runtime guard against a caller that
    // ignores the type system (exactly what used to happen here).
    setSession(undefined, "rt-1", 3600);

    expect(getToken()).toBeNull();
    expect(store["solidping_session_token"]).not.toBe("undefined");
    expect(errorSpy).toHaveBeenCalled();
  });

  it("refuses to persist an empty-string access token", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});

    setSession("", "rt-1", 3600);

    expect(getToken()).toBeNull();
  });
});

// handleResponse must tolerate an empty body under *any* successful status,
// not just 204 — see specs/todos/2026-08-21-04-test-report-send-202-empty-body.md.
// The malformed-body case is the positive control: it proves the guard
// still throws on a genuinely broken JSON payload instead of turning every
// parse failure into a silent `undefined`.
describe("handleResponse", () => {
  const opts = { skipAuth: true, suppress401Redirect: true };

  it("returns undefined for a 204 with no body", async () => {
    const response = new Response(null, { status: 204 });

    await expect(handleResponse<undefined>(response, opts)).resolves.toBeUndefined();
  });

  it("returns undefined for a 202 with an empty body and no Content-Type (bare WriteHeader)", async () => {
    const response = new Response("", { status: 202 });

    await expect(handleResponse<undefined>(response, opts)).resolves.toBeUndefined();
  });

  it("parses a 202 that does carry a JSON body", async () => {
    const response = new Response(JSON.stringify({ queued: true }), {
      status: 202,
      headers: { "Content-Type": "application/json" },
    });

    await expect(handleResponse<{ queued: boolean }>(response, opts)).resolves.toEqual({ queued: true });
  });

  it("throws on a 200 with a malformed JSON body (positive control)", async () => {
    const response = new Response("{not valid json", {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });

    await expect(handleResponse(response, opts)).rejects.toThrow();
  });
});

// The forced-rotation backstop (spec 2026-08-23-04). A flagged session's only
// reachable endpoints are the rotation itself, /auth/me and /auth/logout;
// everything else answers 403 PASSWORD_CHANGE_REQUIRED, and every one of those
// must bounce the browser to the rotation screen. That bounce is what makes the
// screen inescapable — without it, a deep link or a Back press leaves the user
// staring at a generic "Permission denied" with no way forward.
//
// The plain-FORBIDDEN case is the positive control: an ordinary permission
// error must still surface as an ApiError and must NOT navigate anywhere.
describe("handleResponse — forced password rotation", () => {
  const opts = { skipAuth: true, suppress401Redirect: true };

  const stubWindow = (pathname: string) => {
    const location = { pathname, href: "" };
    (globalThis as { window?: unknown }).window = { location };

    return location;
  };

  afterEach(() => {
    delete (globalThis as { window?: unknown }).window;
  });

  const forbidden = (code: string) =>
    new Response(JSON.stringify({ title: "Denied", code }), {
      status: 403,
      headers: { "Content-Type": "application/json" },
    });

  it("redirects to the rotation screen on PASSWORD_CHANGE_REQUIRED", async () => {
    const location = stubWindow(`${DASH_BASE}/orgs/acme/checks`);

    await expect(handleResponse(forbidden("PASSWORD_CHANGE_REQUIRED"), opts)).rejects.toBeInstanceOf(
      ApiError
    );
    expect(location.href).toContain("/change-password");
  });

  it("does not redirect when already on the rotation screen", async () => {
    const basepath = import.meta.env.VITE_BASE_URL || "";
    const location = stubWindow(`${basepath}/change-password`);

    await expect(handleResponse(forbidden("PASSWORD_CHANGE_REQUIRED"), opts)).rejects.toBeInstanceOf(
      ApiError
    );
    expect(location.href).toBe("");
  });

  it("leaves an ordinary FORBIDDEN alone (positive control)", async () => {
    const location = stubWindow(`${DASH_BASE}/orgs/acme/checks`);

    await expect(handleResponse(forbidden("FORBIDDEN"), opts)).rejects.toThrow("Denied");
    expect(location.href).toBe("");
  });
});

// The demo write guard's client half (spec 2026-09-06-02). A shared demo
// session's refused write is NOT a permission problem: the session is valid,
// the demo is simply bounded. Routing to the 403 page would strand a visitor
// on a dead screen; the correct reaction is a toast and an unchanged page.
//
// The plain-FORBIDDEN and PASSWORD_CHANGE_REQUIRED cases above are the positive
// controls for this one: they prove the branch is code-specific rather than
// "every 403 now shows a toast".
describe("handleResponse — DEMO_READ_ONLY", () => {
  const opts = { skipAuth: true, suppress401Redirect: true };

  const stubWindow = (pathname: string) => {
    const location = { pathname, href: "" };
    (globalThis as { window?: unknown }).window = { location };

    return location;
  };

  afterEach(() => {
    delete (globalThis as { window?: unknown }).window;
    vi.restoreAllMocks();
  });

  const forbidden = (code: string, title = "Read-only in the demo") =>
    new Response(JSON.stringify({ title, code }), {
      status: 403,
      headers: { "Content-Type": "application/json" },
    });

  it("throws an ApiError carrying the code, and navigates nowhere", async () => {
    const location = stubWindow(`${DASH_BASE}/orgs/demo/status-pages`);

    await expect(handleResponse(forbidden("DEMO_READ_ONLY"), opts)).rejects.toMatchObject({
      code: "DEMO_READ_ONLY",
      status: 403,
    });

    // The whole point: a refused demo write must never bounce the browser.
    expect(location.href).toBe("");
  });

  it("does not send a demo refusal to the password-rotation screen", async () => {
    const location = stubWindow(`${DASH_BASE}/orgs/demo/checks`);

    await expect(handleResponse(forbidden("DEMO_READ_ONLY"), opts)).rejects.toBeInstanceOf(
      ApiError
    );
    expect(location.href).not.toContain("/change-password");
  });

  // Translate by CODE, not by title (spec
  // 2026-09-07-02-untranslated-strings-and-demo-refusal-message §B.1): the
  // server's `error.title` is always English (kept in the JSON body verbatim
  // for curl/CLI users), so a French dashboard printing `err.message` used to
  // show an English sentence. Asserting against the `fr` bundle rather than
  // hardcoding the string keeps this test honest if the copy changes.
  it("localizes ApiError.message by code, ignoring the server's English title", async () => {
    stubWindow(`${DASH_BASE}/orgs/demo/checks`);
    await i18n.changeLanguage("fr");

    try {
      const err = await handleResponse(forbidden("DEMO_READ_ONLY"), opts).catch(
        (e) => e as ApiError,
      );

      expect(err).toBeInstanceOf(ApiError);
      expect((err as ApiError).message).toBe(frOrg.demo.writeRefused);
      // The server's English title must never leak into the localized UI.
      expect((err as ApiError).message).not.toBe(enOrg.demo.writeRefused);
    } finally {
      await i18n.changeLanguage("en");
    }
  });

  // One surface, one variant (spec §B.2): the toast is the single
  // announcement for a refused demo write, and it must be an info/warning
  // toast — never `toast.error`, since the refusal is not the visitor's
  // mistake.
  it("announces exactly one toast, never toast.error", async () => {
    stubWindow(`${DASH_BASE}/orgs/demo/checks`);
    vi.mocked(toast.info).mockClear();
    vi.mocked(toast.error).mockClear();

    await handleResponse(forbidden("DEMO_READ_ONLY"), opts).catch(() => {});

    expect(toast.info).toHaveBeenCalledTimes(1);
    expect(toast.error).not.toHaveBeenCalled();
  });
});

// AuthContext's logout() sets `loggingOut` before revoking the session, so
// apiFetch's reactive 401 -> refresh-and-retry path is suppressed for
// background requests racing the logout POST (spec 2026-09-25-14). But the
// logout POST itself is what revokes the session server-side, and queued
// spec 2026-09-25-24 (backend deletes the session on logout) depends on that
// POST actually reaching the server authenticated. If the user's access
// token had already expired before they clicked "Sign out" — idle past the
// access-token lifetime, a common case — the POST itself 401s, and a blanket
// suppression would mean it's never retried: the local logout looks
// successful (tokens cleared, redirected to login) while the server-side
// session/refresh token is silently never revoked. `allowRefreshDuringLogout`
// carves out exactly that one call.
describe("apiFetch — logout POST still refreshes despite loggingOut", () => {
  let store: Record<string, string>;

  beforeEach(() => {
    store = {};
    vi.stubGlobal("localStorage", {
      getItem: (k: string) => store[k] ?? null,
      setItem: (k: string, v: string) => {
        store[k] = v;
      },
      removeItem: (k: string) => {
        delete store[k];
      },
    });
    vi.spyOn(console, "error").mockImplementation(() => {});
    vi.spyOn(console, "warn").mockImplementation(() => {});
  });

  afterEach(() => {
    // Module-level state (see client.ts's `loggingOut`) — never let a test
    // that forgot to reach the finally leave it stuck true for later tests
    // in this file.
    setLoggingOut(false);
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it(
    "refreshes and retries the logout POST itself when the access token has " +
      "already expired (positive control — this is exactly what " +
      "allowRefreshDuringLogout exists to fix)",
    async () => {
      setSession("expired-access-token", "valid-refresh-token", 3600);

      const calls: Array<{ url: string; auth: string | null; body: unknown }> = [];
      const fetchSpy = vi.fn(async (url: string, init?: RequestInit) => {
        const headers = new Headers(init?.headers);
        calls.push({
          url,
          auth: headers.get("Authorization"),
          body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
        });

        if (url === "/api/v1/auth/refresh") {
          return new Response(
            JSON.stringify({ accessToken: "new-access-token", expiresIn: 3600 }),
            { status: 200, headers: { "Content-Type": "application/json" } }
          );
        }

        // /api/v1/auth/logout: 401 on the stale token, 204 once retried with
        // the freshly refreshed one.
        if (headers.get("Authorization") === "Bearer expired-access-token") {
          return new Response(null, { status: 401 });
        }
        return new Response(null, { status: 204 });
      });
      vi.stubGlobal("fetch", fetchSpy as unknown as typeof fetch);

      setLoggingOut(true);
      await expect(
        apiFetch("/api/v1/auth/logout", {
          method: "POST",
          allowRefreshDuringLogout: true,
        })
      ).resolves.toBeUndefined();

      // Stale-token attempt, then the refresh, then the retry — in that order.
      expect(calls.map((c) => c.url)).toEqual([
        "/api/v1/auth/logout",
        "/api/v1/auth/refresh",
        "/api/v1/auth/logout",
      ]);
      expect(calls[0].auth).toBe("Bearer expired-access-token");
      expect(calls[1].body).toEqual({ refreshToken: "valid-refresh-token" });
      expect(calls[2].auth).toBe("Bearer new-access-token");
      // The refreshed session is left in place — the retried POST succeeded,
      // so nothing here should have cleared it.
      expect(getToken()).toBe("new-access-token");
      expect(getRefreshToken()).toBe("valid-refresh-token");
    }
  );

  it("does NOT refresh, retry, or log for an ordinary request that 401s while loggingOut is set (no allowRefreshDuringLogout)", async () => {
    setSession("expired-access-token", "valid-refresh-token", 3600);
    const consoleErrorSpy = vi.spyOn(console, "error");

    const fetchSpy = vi.fn(async () => new Response(null, { status: 401 }));
    vi.stubGlobal("fetch", fetchSpy as unknown as typeof fetch);

    setLoggingOut(true);
    await expect(
      apiFetch("/api/v1/orgs/acme/checks", { suppress401Redirect: true })
    ).rejects.toMatchObject({ status: 401 });

    // Exactly the one (failed) request — no refresh attempt was made, so
    // there was nothing for token-refresh.ts's escalate() to log either.
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(consoleErrorSpy).not.toHaveBeenCalled();
  });
});
