import { describe, expect, it } from "vitest";
import { isOrgPublicRoute } from "./org-public-routes";
import { DASH_BASE } from "@/lib/base-path";

describe("isOrgPublicRoute", () => {
  it("matches the org-level login and register routes, with or without a base path", () => {
    expect(isOrgPublicRoute("/orgs/test/login")).toBe(true);
    expect(isOrgPublicRoute(`${DASH_BASE}/orgs/test/login`)).toBe(true);
    expect(isOrgPublicRoute(`${DASH_BASE}/orgs/acmetech/register`)).toBe(true);
  });

  it("does not match nested authenticated routes ending in register", () => {
    expect(
      isOrgPublicRoute(`${DASH_BASE}/orgs/test/organization/private-locations/register`),
    ).toBe(false);
    expect(isOrgPublicRoute(`${DASH_BASE}/orgs/test/checks/abc/login`)).toBe(false);
  });

  it("is independent of which org the route param currently holds", () => {
    // The whole point: the answer may not change just because a param
    // subscription updated one render before the location subscription.
    // Both of these are the login page, whatever `$org` says at this instant.
    for (const org of ["default", "test", "acmetech"]) {
      expect(isOrgPublicRoute(`${DASH_BASE}/orgs/${org}/login`)).toBe(true);
    }
  });

  it("does not match the dashboard or other org routes", () => {
    expect(isOrgPublicRoute(`${DASH_BASE}/orgs/test`)).toBe(false);
    expect(isOrgPublicRoute(`${DASH_BASE}/orgs/test/checks`)).toBe(false);
    expect(isOrgPublicRoute(`${DASH_BASE}/login`)).toBe(false);
  });
});
