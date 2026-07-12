import { describe, expect, it } from "vitest";

import { regionDisplayLabel } from "@/lib/region-label";

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
