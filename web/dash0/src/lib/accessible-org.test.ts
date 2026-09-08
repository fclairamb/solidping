import { describe, expect, it } from "vitest";
import { pickAccessibleOrg, type AccessibleOrgSession } from "./accessible-org";
import type { OrganizationSummary } from "@/contexts/AuthContext";

const orgs = (...slugs: string[]): OrganizationSummary[] =>
  slugs.map((slug) => ({ slug, role: "member" }));

const session = (
  over: Partial<AccessibleOrgSession> = {},
): AccessibleOrgSession => ({
  org: null,
  organizations: [],
  isSuperAdmin: false,
  ...over,
});

describe("pickAccessibleOrg", () => {
  const cases: {
    name: string;
    urlOrg: string;
    session: AccessibleOrgSession;
    want: string | null;
  }[] = [
    {
      name: "a super admin keeps the URL's org even with no membership at all",
      urlOrg: "someone-else",
      session: session({ org: "hq", organizations: [], isSuperAdmin: true }),
      want: "someone-else",
    },
    {
      name: "a super admin keeps the URL's org over their own session org",
      urlOrg: "someone-else",
      session: session({
        org: "hq",
        organizations: orgs("hq"),
        isSuperAdmin: true,
      }),
      want: "someone-else",
    },
    {
      name: "the URL's org is kept when it is a membership",
      urlOrg: "acmetech",
      session: session({ org: "acmetech", organizations: orgs("acmetech") }),
      want: "acmetech",
    },
    {
      name: "the URL's org is kept even when the token names another org (OrgLayout switches instead)",
      urlOrg: "acmetech",
      session: session({ org: "other", organizations: orgs("other", "acmetech") }),
      want: "acmetech",
    },
    {
      name: "a non-member URL falls back to the session org, not to organizations[0]",
      urlOrg: "not-my-org",
      session: session({ org: "acmetech", organizations: orgs("newest", "acmetech") }),
      want: "acmetech",
    },
    {
      name: "the demo case: a demo session on another org's login page goes to the demo org",
      urlOrg: "default",
      session: session({ org: "demo", organizations: orgs("demo") }),
      want: "demo",
    },
    {
      name: "organizations[0] when the session org is no longer a membership",
      urlOrg: "not-my-org",
      session: session({ org: "removed-from", organizations: orgs("newest", "older") }),
      want: "newest",
    },
    {
      name: "organizations[0] when the session carries no org at all",
      urlOrg: "not-my-org",
      session: session({ org: null, organizations: orgs("newest", "older") }),
      want: "newest",
    },
    {
      name: "null when the user belongs to no organization (the caller sends them to /no-org)",
      urlOrg: "not-my-org",
      session: session({ org: null, organizations: [] }),
      want: null,
    },
    {
      name: "null even when the token still names an org the user no longer belongs to",
      urlOrg: "not-my-org",
      session: session({ org: "removed-from", organizations: [] }),
      want: null,
    },
  ];

  for (const c of cases) {
    it(c.name, () => {
      expect(pickAccessibleOrg(c.urlOrg, c.session)).toBe(c.want);
    });
  }

  it("is idempotent: re-running on its own answer is a no-op (no redirect loop)", () => {
    const s = session({ org: "acmetech", organizations: orgs("newest", "acmetech") });
    const first = pickAccessibleOrg("not-my-org", s);
    expect(first).toBe("acmetech");
    expect(pickAccessibleOrg(first as string, s)).toBe(first);
  });
});
