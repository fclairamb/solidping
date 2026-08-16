import { describe, expect, it } from "vitest";

import { regionDisplayLabel, sortRegionSlugs } from "@/lib/region-label";

const REGIONS = [
  { slug: "default", emoji: "🇪🇺", name: "EU1 (default)" },
  { slug: "us-1", emoji: "🇺🇸", name: "US1" },
];

describe("regionDisplayLabel", () => {
  it("renders '{emoji} {name}' for a defined region", () => {
    expect(regionDisplayLabel(REGIONS, "default")).toBe("🇪🇺 EU1 (default)");
    expect(regionDisplayLabel(REGIONS, "us-1")).toBe("🇺🇸 US1");
  });

  it("falls back to the raw slug when the region has no definition", () => {
    expect(regionDisplayLabel(REGIONS, "ap-southeast-9")).toBe("ap-southeast-9");
  });

  it("falls back to the raw slug while definitions are still loading", () => {
    expect(regionDisplayLabel(undefined, "us-1")).toBe("us-1");
  });
});

describe("sortRegionSlugs", () => {
  const MIXED_REGIONS = [
    { slug: "default", emoji: "🇪🇺", name: "EU1 (default)" },
    { slug: "us-1", emoji: "🇺🇸", name: "US1" },
    { slug: "@acmetech/s3ns-paris", emoji: "🇫🇷", name: "S3NS Paris prod", private: true },
    { slug: "@acmetech/aws-lyon", emoji: "🇫🇷", name: "AWS Lyon", private: true },
  ];

  it("puts standard regions before custom/private regions", () => {
    expect(
      sortRegionSlugs(MIXED_REGIONS, ["@acmetech/s3ns-paris", "default"]),
    ).toEqual(["default", "@acmetech/s3ns-paris"]);
  });

  it("no longer lets an @-prefixed slug jump the queue via plain string sort", () => {
    // Plain Array.prototype.sort() would put '@acmetech/s3ns-paris' before
    // 'default' and 'us-1' because '@' < ASCII letters. The canonical order
    // must keep standard regions first regardless of slug spelling.
    const slugs = ["@acmetech/s3ns-paris", "us-1", "default"];
    expect(sortRegionSlugs(MIXED_REGIONS, slugs)).toEqual([
      "default",
      "us-1",
      "@acmetech/s3ns-paris",
    ]);
  });

  it("sorts alphabetically by display name within each group, case-insensitively", () => {
    const standard = [
      { slug: "zebra", emoji: "🏳️", name: "alpha region" },
      { slug: "alpha", emoji: "🏳️", name: "Zebra Region" },
    ];
    expect(sortRegionSlugs(standard, ["alpha", "zebra"])).toEqual([
      "zebra",
      "alpha",
    ]);

    expect(
      sortRegionSlugs(MIXED_REGIONS, [
        "@acmetech/s3ns-paris",
        "@acmetech/aws-lyon",
      ]),
    ).toEqual(["@acmetech/aws-lyon", "@acmetech/s3ns-paris"]);
  });

  it("groups slugs with no matching definition after defined customs, sorted by raw slug", () => {
    expect(
      sortRegionSlugs(MIXED_REGIONS, [
        "zz-unknown",
        "@acmetech/s3ns-paris",
        "default",
        "aa-unknown",
      ]),
    ).toEqual([
      "default",
      "@acmetech/s3ns-paris",
      "aa-unknown",
      "zz-unknown",
    ]);
  });

  it("returns an empty array for an empty input without touching the source array", () => {
    expect(sortRegionSlugs(MIXED_REGIONS, [])).toEqual([]);
  });

  it("works when no region definitions are available yet (all slugs unknown, sorted by slug)", () => {
    expect(sortRegionSlugs(undefined, ["b-slug", "a-slug"])).toEqual([
      "a-slug",
      "b-slug",
    ]);
  });
});
