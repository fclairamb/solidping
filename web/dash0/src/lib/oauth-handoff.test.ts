import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Mirrors token-refresh.test.ts's pattern: mock the localStorage-backed
// api/client setter rather than pull in jsdom for one module.
const setSessionMock = vi.fn();
vi.mock("@/api/client", () => ({
  setSession: (accessToken: string, refreshToken?: string, expiresIn?: number) =>
    setSessionMock(accessToken, refreshToken, expiresIn),
}));

import {
  applyOAuthHandoff,
  parseOAuthHandoff,
  resolveHandoffDestination,
} from "./oauth-handoff";

describe("parseOAuthHandoff", () => {
  it("returns null when there is no access_token param (the common case)", () => {
    expect(parseOAuthHandoff("")).toBeNull();
    expect(parseOAuthHandoff("?org=acme")).toBeNull();
  });

  it("parses a full OAuth redirect (access + refresh + expiry + org)", () => {
    const search = "?access_token=at-1&refresh_token=rt-1&expires_in=3600&org=acme";
    expect(parseOAuthHandoff(search)).toEqual({
      accessToken: "at-1",
      refreshToken: "rt-1",
      expiresIn: 3600,
      org: "acme",
    });
  });

  it("tolerates a missing refresh_token/expires_in/org (still returns the access token)", () => {
    expect(parseOAuthHandoff("?access_token=at-1")).toEqual({
      accessToken: "at-1",
      refreshToken: undefined,
      expiresIn: undefined,
      org: undefined,
    });
  });

  it("treats a non-numeric expires_in as absent rather than NaN", () => {
    const result = parseOAuthHandoff("?access_token=at-1&expires_in=not-a-number");
    expect(result?.expiresIn).toBeUndefined();
  });
});

describe("applyOAuthHandoff", () => {
  let store: Record<string, string>;

  beforeEach(() => {
    store = {};
    setSessionMock.mockClear();
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
  });

  it("persists the full session via setSession — the funnel-audit fix for the OAuth handoff", () => {
    applyOAuthHandoff({
      accessToken: "at-1",
      refreshToken: "rt-1",
      expiresIn: 3600,
      org: "acme",
    });

    // This is the regression this test guards: the OAuth handoff used to
    // call a since-removed setToken(accessToken) that dropped the refresh
    // token and expiry entirely (see main.tsx's history — the 2026-07-08
    // zombie-socket incident's leading hypothesis).
    expect(setSessionMock).toHaveBeenCalledWith("at-1", "rt-1", 3600);
    expect(store["solidping_org"]).toBe("acme");
  });

  it("still persists the access token when refresh_token/org are absent", () => {
    applyOAuthHandoff({ accessToken: "at-1" });

    expect(setSessionMock).toHaveBeenCalledWith("at-1", undefined, undefined);
    expect(store["solidping_org"]).toBeUndefined();
  });
});

describe("resolveHandoffDestination", () => {
  const BASE = "/dash0";

  it("preserves a deep returnTo path whose org matches the handoff org", () => {
    // The core regression: after the provider round-trip the browser lands on
    // the deep path (already in window.location.pathname); it must be kept,
    // not replaced by the org root.
    expect(
      resolveHandoffDestination("acme", "/dash0/orgs/acme/checks/foo", BASE),
    ).toBe("/dash0/orgs/acme/checks/foo");
  });

  it("keeps the org root when the deep path already is the org root", () => {
    expect(resolveHandoffDestination("acme", "/dash0/orgs/acme", BASE)).toBe(
      "/dash0/orgs/acme",
    );
  });

  it("falls back to the org root for a cross-org returnTo path", () => {
    expect(
      resolveHandoffDestination("acme", "/dash0/orgs/other/checks", BASE),
    ).toBe("/dash0/orgs/acme");
  });

  it("falls back to the org root for a non-org path (Discord's redirect_uri=/)", () => {
    // Discord defaults redirect_uri to "/" but buildSuccessRedirect still sets
    // org, so we land the user on the org root — never on "/".
    expect(resolveHandoffDestination("acme", "/", BASE)).toBe(
      "/dash0/orgs/acme",
    );
    expect(resolveHandoffDestination("acme", "/dash0", BASE)).toBe(
      "/dash0/orgs/acme",
    );
  });

  it("falls back to the org root for an unsafe protocol-relative path", () => {
    expect(
      resolveHandoffDestination("acme", "//evil.com/dash0/orgs/acme", BASE),
    ).toBe("/dash0/orgs/acme");
  });

  it("keeps the current pathname when no org was handed off", () => {
    // Nothing to resolve against — mirrors the previous fallback so behaviour
    // is unchanged for the (unexpected) org-less redirect.
    expect(
      resolveHandoffDestination(undefined, "/dash0/orgs/acme/checks", BASE),
    ).toBe("/dash0/orgs/acme/checks");
  });

  it("works with an empty base path", () => {
    expect(
      resolveHandoffDestination("acme", "/orgs/acme/incidents", ""),
    ).toBe("/orgs/acme/incidents");
    expect(resolveHandoffDestination("acme", "/orgs/other", "")).toBe(
      "/orgs/acme",
    );
  });
});
