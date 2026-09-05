import { describe, expect, it } from "vitest";

import { ORG_SLUG_MAX_LEN, orgSlugify } from "@/lib/org-slug";

// These expectations are the OUTPUT OF THE SERVER's orgslug.Slugify
// (server/internal/orgslug/orgslug.go) for the same inputs. /no-org submits the
// org with no slug and lets the server derive one, while the form previews
// "will be reachable as …" from this function — so a divergence here is a
// preview that lies about the address the user is about to get.
describe("orgSlugify mirrors the server's orgslug.Slugify", () => {
  it("drops apostrophes rather than turning them into hyphens", () => {
    // The bug this pins: a generic /[^a-z0-9]+/ -> "-" regex yields
    // "alice-s-organization"; the server yields "alices-organization".
    expect(orgSlugify("Alice's organization")).toBe("alices-organization");
    expect(orgSlugify("O'Brien Co")).toBe("obrien-co");
  });

  it("lowercases and turns spaces into hyphens", () => {
    expect(orgSlugify("Bright Falcon")).toBe("bright-falcon");
    expect(orgSlugify("ACME Corp")).toBe("acme-corp");
  });

  it("collapses repeated hyphens and trims the ends", () => {
    expect(orgSlugify("  Acme   Corp  ")).toBe("acme-corp");
    expect(orgSlugify("--acme--corp--")).toBe("acme-corp");
  });

  it("returns empty for anything that normalizes below the 3-char floor", () => {
    expect(orgSlugify("")).toBe("");
    expect(orgSlugify("!!")).toBe("");
    expect(orgSlugify("ab")).toBe("");
    expect(orgSlugify("李雷")).toBe("");
  });

  it("caps at the max length and trims a hyphen the cap introduced", () => {
    const long = orgSlugify("Extraordinarily Long Organization Name");
    expect(long.length).toBeLessThanOrEqual(ORG_SLUG_MAX_LEN);
    expect(long.endsWith("-")).toBe(false);
    expect(long).toBe("extraordinarily-long");
  });
});
