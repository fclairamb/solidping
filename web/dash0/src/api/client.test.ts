import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, getToken, handleResponse, setSession } from "./client";

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
    const location = stubWindow("/dash0/orgs/acme/checks");

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
    const location = stubWindow("/dash0/orgs/acme/checks");

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
    const location = stubWindow("/dash0/orgs/demo/status-pages");

    await expect(handleResponse(forbidden("DEMO_READ_ONLY"), opts)).rejects.toMatchObject({
      code: "DEMO_READ_ONLY",
      status: 403,
    });

    // The whole point: a refused demo write must never bounce the browser.
    expect(location.href).toBe("");
  });

  it("does not send a demo refusal to the password-rotation screen", async () => {
    const location = stubWindow("/dash0/orgs/demo/checks");

    await expect(handleResponse(forbidden("DEMO_READ_ONLY"), opts)).rejects.toBeInstanceOf(
      ApiError
    );
    expect(location.href).not.toContain("/change-password");
  });
});
