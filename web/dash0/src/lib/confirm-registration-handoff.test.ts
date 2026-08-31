import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// Mirrors oauth-handoff.test.ts's pattern: mock the localStorage-backed
// api/client setter rather than pull in jsdom for one module.
const setSessionMock = vi.fn();
vi.mock("@/api/client", () => ({
  setSession: (accessToken: string, refreshToken?: string, expiresIn?: number) =>
    setSessionMock(accessToken, refreshToken, expiresIn),
}));

import {
  applyConfirmRegistrationHandoff,
  ConfirmRegistrationSessionError,
} from "./confirm-registration-handoff";

describe("applyConfirmRegistrationHandoff", () => {
  beforeEach(() => {
    setSessionMock.mockClear();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("persists the full session (access + refresh + expiry) — the 2026-07-08 funnel-audit fix", () => {
    applyConfirmRegistrationHandoff({
      accessToken: "at-1",
      refreshToken: "rt-1",
      expiresIn: 3600,
      organization: { uid: "org-1", slug: "acme" },
    });

    // This is the regression this test guards: confirm-registration used to
    // call setSession with only the access token (or a since-removed
    // setToken), silently dropping the refresh token and expiry — so the
    // session died on first access-token expiry with no way to refresh.
    expect(setSessionMock).toHaveBeenCalledWith("at-1", "rt-1", 3600);
  });

  it("still persists the access token when refresh_token/expiry are absent", () => {
    applyConfirmRegistrationHandoff({ accessToken: "at-1" });

    expect(setSessionMock).toHaveBeenCalledWith("at-1", undefined, undefined);
  });

  it("returns the org nav target when the response includes an organization", () => {
    const target = applyConfirmRegistrationHandoff({
      accessToken: "at-1",
      refreshToken: "rt-1",
      expiresIn: 3600,
      organization: { uid: "org-1", slug: "acme" },
    });

    expect(target).toEqual({ to: "/orgs/$org", params: { org: "acme" } });
  });

  it("returns the no-org nav target when the response has no organization", () => {
    const target = applyConfirmRegistrationHandoff({
      accessToken: "at-1",
      refreshToken: "rt-1",
      expiresIn: 3600,
    });

    expect(target).toEqual({ to: "/no-org" });
  });

  // The regression this spec (2026-08-29-06) exists for: the backend's
  // zero-org confirm-registration branch used to omit accessToken
  // entirely, and this function forwarded `undefined` straight to
  // setSession — which then persisted the literal string "undefined" as
  // the session token, and the very next authenticated call 401'd the
  // freshly-created user back to the login page.
  it("never calls setSession when the response carries no accessToken", () => {
    expect(() => applyConfirmRegistrationHandoff({})).toThrow(
      ConfirmRegistrationSessionError
    );

    expect(setSessionMock).not.toHaveBeenCalled();
  });

  it("throws ConfirmRegistrationSessionError (not a generic Error) so the route can distinguish it from a bad token", () => {
    let caught: unknown;
    try {
      applyConfirmRegistrationHandoff({ accessToken: "" });
    } catch (err) {
      caught = err;
    }

    expect(caught).toBeInstanceOf(ConfirmRegistrationSessionError);
  });
});
