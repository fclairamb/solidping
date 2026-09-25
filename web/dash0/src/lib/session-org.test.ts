import { describe, expect, it } from "vitest";
import { persistSessionOrg, SESSION_ORG_KEY } from "./session-org";
import { needsOrgSwitch } from "./org-switch";

/** A Map-backed stand-in for localStorage (the unit tests run in node). */
function memoryStorage(initial: Record<string, string> = {}) {
  const map = new Map(Object.entries(initial));
  return {
    map,
    setItem: (key: string, value: string) => void map.set(key, value),
    removeItem: (key: string) => void map.delete(key),
  };
}

describe("persistSessionOrg", () => {
  it("stores and returns the org of an org-scoped response", () => {
    const storage = memoryStorage({ [SESSION_ORG_KEY]: "older" });
    expect(persistSessionOrg({ slug: "acmetech" }, storage)).toBe("acmetech");
    expect(storage.map.get(SESSION_ORG_KEY)).toBe("acmetech");
  });

  // Spec 2026-09-25-15: the stale slug is exactly the one that would make an
  // org-less session look already scoped to that org.
  it("clears a stale stored org on an org-less response", () => {
    for (const organization of [undefined, null, {}, { slug: "" }]) {
      const storage = memoryStorage({ [SESSION_ORG_KEY]: "acmetech" });
      expect(persistSessionOrg(organization, storage)).toBeNull();
      expect(storage.map.has(SESSION_ORG_KEY)).toBe(false);
    }
  });

  it("lets OrgLayout switch into the stale org instead of trusting it", () => {
    const storage = memoryStorage({ [SESSION_ORG_KEY]: "acmetech" });
    const session = {
      isAuthenticated: true,
      isLoading: false,
      organizations: [{ slug: "acmetech", role: "user" }],
      isSuperAdmin: false,
    };

    // Before the fix: auth.org kept the stale slug and no switch ran.
    expect(needsOrgSwitch("acmetech", { ...session, org: "acmetech" }, false)).toBe(false);

    // The org-less response now resets auth.org, so the switch fires.
    const org = persistSessionOrg(undefined, storage);
    expect(needsOrgSwitch("acmetech", { ...session, org }, false)).toBe(true);
  });
});
