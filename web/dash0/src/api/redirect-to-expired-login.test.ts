/**
 * @vitest-environment jsdom
 *
 * Spec 2026-08-29-12 fixed `/orgs/:org/register` 401-bouncing an
 * unauthenticated visitor to `/login`. The first attempt at hardening
 * `redirectToExpiredLogin`'s no-op guard *replaced* the bare
 * `endsWith("/login")` check with `isOrgPublicRoute` instead of unioning the
 * two — which fixed org-register but reintroduced the exact same bug on the
 * separate root `/login` route (`src/routes/login.tsx`): a 401 raised while
 * sitting there would fall through and bounce the user off the login page
 * they were already on with a spurious "session expired".
 *
 * These three no-op cases are the full set `redirectToExpiredLogin` must
 * cover, paired with a positive control proving it still redirects normally
 * from an ordinary authenticated route. Assert on the actual side effect
 * (`window.location.href`), not on navigation — jsdom doesn't navigate.
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { redirectToExpiredLogin } from "./client";

function setLocation(pathname: string): void {
  Object.defineProperty(window, "location", {
    configurable: true,
    value: {
      pathname,
      search: "",
      href: `http://localhost${pathname}`,
    },
  });
}

describe("redirectToExpiredLogin", () => {
  const originalLocation = window.location;

  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    Object.defineProperty(window, "location", {
      configurable: true,
      value: originalLocation,
    });
  });

  it.each([
    ["/dash0/orgs/test/login", "org-level login"],
    ["/dash0/orgs/test/register", "org-level register"],
    ["/dash0/login", "root login"],
  ])("no-ops on %s (%s)", (pathname) => {
    setLocation(pathname);
    const before = window.location.href;

    redirectToExpiredLogin();

    expect(window.location.href).toBe(before);
  });

  it("still redirects to the org login page from an ordinary authenticated route", () => {
    setLocation("/dash0/orgs/test/checks");

    redirectToExpiredLogin();

    expect(window.location.href).toContain("/orgs/test/login");
    expect(window.location.href).toContain("session_expired=true");
  });
});
