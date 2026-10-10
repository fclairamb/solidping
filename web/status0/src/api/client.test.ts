import { afterEach, describe, expect, test } from "bun:test";
import { apiFetch } from "./client";

const realFetch = globalThis.fetch;

describe("apiFetch", () => {
  afterEach(() => {
    globalThis.fetch = realFetch;
  });

  test("always asks the server instead of the browser's HTTP cache", async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    globalThis.fetch = (async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      return new Response(JSON.stringify({ ok: true }), { status: 200 });
    }) as typeof fetch;

    await apiFetch("/api/v1/status-pages/acme");

    expect(calls).toHaveLength(1);
    expect(calls[0].init?.cache).toBe("no-cache");
  });
});
