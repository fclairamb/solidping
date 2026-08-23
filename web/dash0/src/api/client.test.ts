import { afterEach, describe, expect, it } from "vitest";

import { ApiError, handleResponse } from "./client";

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
