import { describe, expect, it } from "vitest";
import { canRenderBareOutlet, isOrgPublicRoute } from "./org-public-routes";
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

describe("canRenderBareOutlet", () => {
  it("is false while a sidebar page is still committed under a pending login URL", () => {
    // An expired session on a deep link: the location already says /login,
    // the outlet still renders the page being left. Rendering it bare threw
    // "useSidebar must be used within a SidebarProvider".
    expect(
      canRenderBareOutlet(
        `${DASH_BASE}/orgs/acmetech/login`,
        `${DASH_BASE}/orgs/acmetech/design-reference`,
      ),
    ).toBe(false);
    expect(canRenderBareOutlet("/orgs/acmetech/login", undefined)).toBe(false);
  });

  it("is true once the login match is committed, including a login to login org switch", () => {
    expect(canRenderBareOutlet("/orgs/acmetech/login", "/orgs/acmetech/login")).toBe(true);
    expect(canRenderBareOutlet("/orgs/test/login", "/orgs/default/login")).toBe(true);
    expect(canRenderBareOutlet("/orgs/acmetech/register", "/orgs/acmetech/register")).toBe(true);
  });

  it("is false for any non-public pending URL", () => {
    expect(canRenderBareOutlet("/orgs/acmetech/checks", "/orgs/acmetech/login")).toBe(false);
  });
});
