import { describe, expect, it } from "vitest";
import { needsOrgSwitch, type OrgSwitchSession } from "./org-switch";
import { pickAccessibleOrg } from "./accessible-org";
import type { OrganizationSummary } from "@/contexts/AuthContext";

const orgs = (...slugs: string[]): OrganizationSummary[] =>
  slugs.map((slug) => ({ slug, role: "user" }));

const session = (over: Partial<OrgSwitchSession> = {}): OrgSwitchSession => ({
  isAuthenticated: true,
  isLoading: false,
  org: null,
  organizations: [],
  isSuperAdmin: false,
  ...over,
});

describe("needsOrgSwitch", () => {
  const cases: {
    name: string;
    urlOrg: string;
    session: OrgSwitchSession;
    isPublicRoute?: boolean;
    want: boolean;
  }[] = [
    {
      // THE CASE spec 2026-09-25-15 exists for: an org-less session (a
      // federated login another org refused) opening an org it belongs to.
      name: "an org-less session that belongs to the URL's org switches",
      urlOrg: "acmetech",
      session: session({ org: null, organizations: orgs("acmetech") }),
      want: true,
    },
    {
      name: "a session scoped to another org the user belongs to switches",
      urlOrg: "acmetech",
      session: session({ org: "other", organizations: orgs("other", "acmetech") }),
      want: true,
    },
    {
      name: "a session already scoped to the URL's org does not switch",
      urlOrg: "acmetech",
      session: session({ org: "acmetech", organizations: orgs("acmetech") }),
      want: false,
    },
    {
      name: "an org-less session that does NOT belong to the URL's org does not switch",
      urlOrg: "demo",
      session: session({ org: null, organizations: orgs("acmetech") }),
      want: false,
    },
    {
      name: "an org-less session with no organization at all does not switch",
      urlOrg: "demo",
      session: session({ org: null, organizations: [] }),
      want: false,
    },
    {
      name: "a super admin never switches",
      urlOrg: "acmetech",
      session: session({ org: null, organizations: orgs("acmetech"), isSuperAdmin: true }),
      want: false,
    },
    {
      name: "nothing is decided while the session is loading",
      urlOrg: "acmetech",
      session: session({ isLoading: true, organizations: orgs("acmetech") }),
      want: false,
    },
    {
      name: "an unauthenticated visitor never switches",
      urlOrg: "acmetech",
      session: session({ isAuthenticated: false, organizations: orgs("acmetech") }),
      want: false,
    },
    {
      name: "the org's own login page never switches",
      urlOrg: "acmetech",
      session: session({ org: null, organizations: orgs("acmetech") }),
      isPublicRoute: true,
      want: false,
    },
  ];

  for (const c of cases) {
    it(c.name, () => {
      expect(needsOrgSwitch(c.urlOrg, c.session, c.isPublicRoute ?? false)).toBe(c.want);
    });
  }

  it("never overlaps the non-member redirect of spec 2026-09-08-01", () => {
    // Whenever a switch is needed, pickAccessibleOrg keeps the URL's org, so
    // OrgLayout's two gates can never both fire and bounce the user around.
    for (const c of cases) {
      if (!needsOrgSwitch(c.urlOrg, c.session, c.isPublicRoute ?? false)) continue;
      expect(
        pickAccessibleOrg(c.urlOrg, {
          org: c.session.org,
          organizations: c.session.organizations,
          isSuperAdmin: c.session.isSuperAdmin,
        }),
      ).toBe(c.urlOrg);
    }
  });
});
