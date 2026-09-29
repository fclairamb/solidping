/**
 * @vitest-environment jsdom
 *
 * Super-admin impersonation, client side (spec 2026-09-29-03).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { clearToken, getToken } from "@/api/client";
import {
  canImpersonate,
  clearImpersonation,
  exitDestination,
  exitImpersonation,
  getImpersonation,
  getImpersonationToken,
  IMPERSONATION_KEY,
  isImpersonationExpired,
  startImpersonation,
  type ImpersonationSession,
} from "@/lib/impersonation";

const SESSION: ImpersonationSession = {
  accessToken: "imp-token",
  expiresAt: Date.now() + 30 * 60 * 1000,
  targetEmail: "alice@acme.com",
  orgSlug: "acme",
  returnPath: "/orgs/default/server/users",
};

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("impersonation storage", () => {
  it("stores the session in sessionStorage, never localStorage", () => {
    startImpersonation(SESSION);

    expect(getImpersonation()).toEqual(SESSION);
    expect(getImpersonationToken()).toBe("imp-token");
    expect(sessionStorage.getItem(IMPERSONATION_KEY)).not.toBeNull();
    expect(JSON.stringify({ ...localStorage })).not.toContain("imp-token");
  });

  it("drops a malformed entry", () => {
    sessionStorage.setItem(IMPERSONATION_KEY, "{not json");
    expect(getImpersonation()).toBeNull();
    expect(sessionStorage.getItem(IMPERSONATION_KEY)).toBeNull();

    sessionStorage.setItem(IMPERSONATION_KEY, JSON.stringify({ ...SESSION, returnPath: "https://evil.example" }));
    expect(getImpersonation()).toBeNull();
  });

  it("clearImpersonation returns what was active", () => {
    startImpersonation(SESSION);
    expect(clearImpersonation()).toEqual(SESSION);
    expect(getImpersonation()).toBeNull();
  });

  it("knows when the token has run out", () => {
    expect(isImpersonationExpired(SESSION, SESSION.expiresAt - 1)).toBe(false);
    expect(isImpersonationExpired(SESSION, SESSION.expiresAt)).toBe(true);
  });
});

describe("the request token", () => {
  it("prefers the impersonation token and keeps the admin's session intact", () => {
    localStorage.setItem("solidping_session_token", "admin-token");
    localStorage.setItem("solidping_refresh_token", "admin-refresh");

    expect(getToken()).toBe("admin-token");

    startImpersonation(SESSION);
    expect(getToken()).toBe("imp-token");
    expect(localStorage.getItem("solidping_session_token")).toBe("admin-token");
    expect(localStorage.getItem("solidping_refresh_token")).toBe("admin-refresh");

    // Exit = drop the tab's token; the admin's own comes straight back.
    clearImpersonation();
    expect(getToken()).toBe("admin-token");
  });

  it("a cleared session takes the impersonation with it", () => {
    startImpersonation(SESSION);
    clearToken();
    expect(getImpersonation()).toBeNull();
  });
});

describe("exit", () => {
  it("returns to the page the admin started from", () => {
    expect(exitDestination(SESSION)).toBe("/d/orgs/default/server/users");
    expect(exitDestination(null)).toBe("/d/");
    expect(exitDestination({ ...SESSION, returnPath: "//evil.example" })).toBe("/d/");
  });

  it("drops the token and navigates", () => {
    const assign = vi.fn();
    vi.spyOn(window, "location", "get").mockReturnValue({
      ...window.location,
      assign,
    } as Location);

    startImpersonation(SESSION);
    exitImpersonation();

    expect(getImpersonation()).toBeNull();
    expect(assign).toHaveBeenCalledWith("/d/orgs/default/server/users");
  });
});

describe("canImpersonate", () => {
  const admin = { uid: "admin", isSuperAdmin: true };
  const row = { uid: "alice", superAdmin: false, demo: false, orgs: [{}] };

  it("offers an ordinary member to a super admin", () => {
    expect(canImpersonate(row, admin)).toBe(true);
  });

  it("never offers it to a normal user", () => {
    expect(canImpersonate(row, { uid: "bob", isSuperAdmin: false })).toBe(false);
    expect(canImpersonate(row, null)).toBe(false);
  });

  it("hides super admins, yourself, the demo account and org-less users", () => {
    expect(canImpersonate({ ...row, superAdmin: true }, admin)).toBe(false);
    expect(canImpersonate({ ...row, uid: "admin" }, admin)).toBe(false);
    expect(canImpersonate({ ...row, demo: true }, admin)).toBe(false);
    expect(canImpersonate({ ...row, orgs: [] }, admin)).toBe(false);
  });
});
