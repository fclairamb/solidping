import { describe, expect, it } from "vitest";

import { handleResponse } from "./client";

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
