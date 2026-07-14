import { describe, expect, it } from "vitest";

import { resolveCheckRefLabel } from "@/lib/dependency-graph";

// Regression coverage for issue #129: a dependency row used to render as a
// bare kind badge with no check name when both `name` and `slug` were empty
// (the check behind the edge had been deleted, so the API's zero-value
// CheckRef{} slipped through unnoticed). resolveCheckRefLabel is the single
// place both the dependencies card and the check form's parent list resolve
// a display label, so this pins down every fallback tier.
describe("resolveCheckRefLabel", () => {
  it("prefers name when present", () => {
    expect(
      resolveCheckRefLabel({ uid: "u1", slug: "my-slug", name: "My Name" }),
    ).toBe("My Name");
  });

  it("falls back to slug when name is empty", () => {
    expect(resolveCheckRefLabel({ uid: "u1", slug: "my-slug", name: "" })).toBe(
      "my-slug",
    );
  });

  it("falls back to uid when name and slug are both empty", () => {
    expect(resolveCheckRefLabel({ uid: "u1", slug: "", name: "" })).toBe("u1");
  });

  it("returns empty string when the ref never resolved to a real check (the #129 case)", () => {
    expect(resolveCheckRefLabel({ uid: "", slug: "", name: "" })).toBe("");
  });

  it("returns empty string for a missing ref", () => {
    expect(resolveCheckRefLabel(undefined)).toBe("");
    expect(resolveCheckRefLabel(null)).toBe("");
  });
});
