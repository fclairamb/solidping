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

  test("takes a read the shell started once, then goes to the network", async () => {
    const calls: string[] = [];
    globalThis.fetch = (async (url: string) => {
      calls.push(url);
      return new Response(JSON.stringify({ from: "network" }), { status: 200 });
    }) as typeof fetch;
    const url = "/api/v1/status-pages/acme?include=";
    (globalThis as { window?: unknown }).window = {
      __spPrefetch: {
        [url]: Promise.resolve(
          new Response(JSON.stringify({ from: "shell" }), { status: 200 }),
        ),
      },
    };

    try {
      expect(await apiFetch<{ from: string }>(url)).toEqual({ from: "shell" });
      expect(calls).toHaveLength(0);
      expect(await apiFetch<{ from: string }>(url)).toEqual({ from: "network" });
      expect(calls).toEqual([url]);
    } finally {
      delete (globalThis as { window?: unknown }).window;
    }
  });
});
