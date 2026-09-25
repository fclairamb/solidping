import { beforeEach, describe, expect, it, vi } from "vitest";

const apiFetchMock = vi.fn();
vi.mock("@/api/client", () => ({
  apiFetch: (...args: unknown[]) => apiFetchMock(...args),
}));

import {
  exchangeHandoffCode,
  handoffLandingState,
  membershipPendingNotice,
  resolveHandoffLanding,
} from "./auth-handoff";
import { DASH_BASE } from "@/lib/base-path";

const BASE = DASH_BASE;
const acme = { uid: "org-uid", slug: "acme" };

describe("resolveHandoffLanding", () => {
  it("keeps an in-app deep link in the session's org", () => {
    expect(
      resolveHandoffLanding(
        { organization: acme, returnTo: `${BASE}/orgs/acme/checks?status=down` },
        undefined,
        BASE,
      ),
    ).toEqual({ href: `${BASE}/orgs/acme/checks?status=down` });
  });

  it("lands on the org root when there is no returnTo", () => {
    expect(resolveHandoffLanding({ organization: acme }, undefined, BASE)).toEqual({
      to: "/orgs/$org",
      params: { org: "acme" },
    });
  });

  it("never follows a returnTo into another org or off-site", () => {
    for (const returnTo of [
      `${BASE}/orgs/other/checks`,
      "https://evil.example/d/orgs/acme",
      "//evil.example/d/orgs/acme",
      "/", // Discord's default redirect_uri
    ]) {
      expect(resolveHandoffLanding({ organization: acme, returnTo }, undefined, BASE)).toEqual({
        to: "/orgs/$org",
        params: { org: "acme" },
      });
    }
  });

  it("resumes the MCP consent flow through the login page", () => {
    const returnTo = `${BASE}/orgs/acme/login?returnTo=${encodeURIComponent("/api/v1/oauth/authorize?client_id=x")}`;
    expect(resolveHandoffLanding({ organization: acme, returnTo }, undefined, BASE)).toEqual({
      href: returnTo,
    });
  });

  it("sends an org-less session to /no-org, naming the pending org", () => {
    expect(
      resolveHandoffLanding({ membershipPending: "acme", returnTo: `${BASE}/orgs/acme` }, undefined, BASE),
    ).toEqual({ to: "/no-org", search: { membershipPending: "acme" } });
  });

  it("falls back to the URL's membershipPending, and names nothing without one", () => {
    expect(resolveHandoffLanding({}, "acme", BASE)).toEqual({
      to: "/no-org",
      search: { membershipPending: "acme" },
    });
    expect(resolveHandoffLanding({}, undefined, BASE)).toEqual({
      to: "/no-org",
      search: { membershipPending: undefined },
    });
  });
});

// Spec 2026-09-25-15: refused by the org the login started from, but a member
// of another one — the session is scoped to that other org.
describe("a pending login that landed on the user's own org", () => {
  const own = { uid: "own-uid", slug: "acmetech" };

  it("lands on the user's own org, dropping the refused org's returnTo", () => {
    expect(
      resolveHandoffLanding(
        { organization: own, membershipPending: "demo", returnTo: `${BASE}/orgs/demo` },
        "demo",
        BASE,
      ),
    ).toEqual({ to: "/orgs/$org", params: { org: "acmetech" } });
  });

  it("names the pending org and the landing org in the notice", () => {
    expect(membershipPendingNotice({ organization: own, membershipPending: "demo" }, undefined)).toEqual({
      pending: "demo",
      org: "acmetech",
    });
    // The URL's flag is the fallback, as for the landing.
    expect(membershipPendingNotice({ organization: own }, "demo")).toEqual({
      pending: "demo",
      org: "acmetech",
    });
  });

  it("says nothing without a pending org, for an org-less session, or for the same org", () => {
    expect(membershipPendingNotice({ organization: own }, undefined)).toBeNull();
    // An org-less session lands on /no-org, which already says it.
    expect(membershipPendingNotice({ membershipPending: "demo" }, "demo")).toBeNull();
    expect(membershipPendingNotice({ organization: own, membershipPending: "acmetech" }, undefined)).toBeNull();
  });
});

describe("handoffLandingState", () => {
  it("has nothing to do before a landing is resolved", () => {
    expect(handoffLandingState(false, { isAuthenticated: true, isLoading: false })).toBe("none");
  });

  it("waits while the session is resolving", () => {
    expect(handoffLandingState(true, { isAuthenticated: false, isLoading: true })).toBe("wait");
    expect(handoffLandingState(true, { isAuthenticated: true, isLoading: true })).toBe("wait");
  });

  it("goes once the stored session is authenticated", () => {
    expect(handoffLandingState(true, { isAuthenticated: true, isLoading: false })).toBe("go");
  });

  // A session wiped after the exchange (a concurrent validateSession 401)
  // must not leave "Finishing sign-in…" spinning forever.
  it("is stranded when the session resolved signed-out", () => {
    expect(handoffLandingState(true, { isAuthenticated: false, isLoading: false })).toBe("stranded");
  });
});

describe("exchangeHandoffCode", () => {
  beforeEach(() => {
    apiFetchMock.mockReset();
  });

  it("posts the code once, unauthenticated, even when asked twice", async () => {
    const response = { accessToken: "at", user: { uid: "u", email: "e", role: "user" } };
    apiFetchMock.mockResolvedValue(response);

    const [first, second] = await Promise.all([
      exchangeHandoffCode("code-once"),
      exchangeHandoffCode("code-once"),
    ]);

    expect(first).toBe(response);
    expect(second).toBe(response);
    expect(apiFetchMock).toHaveBeenCalledTimes(1);
    expect(apiFetchMock).toHaveBeenCalledWith("/api/v1/auth/handoff/exchange", {
      method: "POST",
      body: JSON.stringify({ code: "code-once" }),
      skipAuth: true,
    });
  });

  it("keeps different codes apart", async () => {
    apiFetchMock.mockResolvedValue({});

    await exchangeHandoffCode("code-a");
    await exchangeHandoffCode("code-b");

    expect(apiFetchMock).toHaveBeenCalledTimes(2);
  });
});
