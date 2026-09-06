import { describe, expect, it } from "vitest";

import { CHANGELOG_URL, marketingSiteUrl } from "@/lib/marketing-url";

// Exact-URL assertions on purpose: this is the first UTM convention in the
// repo (spec 2026-09-06-01), so any drift in a param name or value should
// fail loudly rather than pass a looser "contains" check.
describe("marketingSiteUrl", () => {
  it("tags a saas visitor", () => {
    expect(marketingSiteUrl("saas")).toBe(
      "https://www.solidping.io/?utm_source=solidping-dashboard&utm_medium=app&utm_campaign=saas&utm_content=login-footer",
    );
  });

  it("tags a self-hosted visitor", () => {
    expect(marketingSiteUrl("self-hosted")).toBe(
      "https://www.solidping.io/?utm_source=solidping-dashboard&utm_medium=app&utm_campaign=self-hosted&utm_content=login-footer",
    );
  });

  it("falls back to self-hosted when the deployment mode is unknown", () => {
    expect(marketingSiteUrl(undefined)).toBe(
      "https://www.solidping.io/?utm_source=solidping-dashboard&utm_medium=app&utm_campaign=self-hosted&utm_content=login-footer",
    );
  });

  it("accepts a different utm_content for a future placement", () => {
    expect(marketingSiteUrl("saas", "account-menu")).toBe(
      "https://www.solidping.io/?utm_source=solidping-dashboard&utm_medium=app&utm_campaign=saas&utm_content=account-menu",
    );
  });
});

describe("CHANGELOG_URL", () => {
  it("is the unadorned docs changelog page", () => {
    expect(CHANGELOG_URL).toBe("https://solidping.io/docs/changelog");
  });
});
