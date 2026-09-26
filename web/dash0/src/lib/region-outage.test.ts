import { describe, expect, it } from "vitest";

import type { RegionDefinition } from "@/api/hooks";
import {
  checkRegionOutage,
  offlineRegionNames,
  offlineRegions,
  summarizeRegionOutage,
} from "@/lib/region-outage";

const regions: RegionDefinition[] = [
  { slug: "paris", emoji: "🇫🇷", name: "Paris", status: "online" },
  {
    slug: "lauterbourg",
    emoji: "🇫🇷",
    name: "Lauterbourg",
    status: "offline",
    offlineSince: "2026-09-24T13:41:00Z",
  },
  {
    slug: "frankfurt",
    emoji: "🇩🇪",
    name: "Frankfurt",
    status: "offline",
    offlineSince: "2026-09-24T12:00:00Z",
  },
  { slug: "@datacenter", emoji: "🏢", name: "Datacenter", private: true },
];

describe("checkRegionOutage", () => {
  it("is blind when every region of the check is offline", () => {
    const outage = checkRegionOutage({ regions: ["lauterbourg"] }, regions);
    expect(outage?.kind).toBe("blind");
    expect(outage?.since).toBe("2026-09-24T13:41:00Z");
    expect(outage?.liveCount).toBe(0);
  });

  it("is reduced when some regions still run", () => {
    const outage = checkRegionOutage({ regions: ["lauterbourg", "paris", "@datacenter"] }, regions);
    expect(outage?.kind).toBe("reduced");
    expect(outage?.liveCount).toBe(2);
    expect(outage?.totalCount).toBe(3);
  });

  it("dates a multi-region outage from its earliest region", () => {
    const outage = checkRegionOutage({ regions: ["lauterbourg", "frankfurt"] }, regions);
    expect(outage?.kind).toBe("blind");
    expect(outage?.since).toBe("2026-09-24T12:00:00Z");
  });

  it("ignores online, private, any-region and disabled checks", () => {
    expect(checkRegionOutage({ regions: ["paris"] }, regions)).toBeNull();
    expect(checkRegionOutage({ regions: ["@datacenter"] }, regions)).toBeNull();
    expect(checkRegionOutage({ regions: [] }, regions)).toBeNull();
    expect(checkRegionOutage({ regions: ["lauterbourg"], enabled: false }, regions)).toBeNull();
  });

  it("treats a server without the status field as all online", () => {
    const legacy = regions.map(({ slug, emoji, name }) => ({ slug, emoji, name }));
    expect(checkRegionOutage({ regions: ["lauterbourg"] }, legacy)).toBeNull();
    expect(offlineRegions(legacy)).toEqual([]);
  });
});

describe("summarizeRegionOutage", () => {
  it("splits the listed checks into blind and reduced", () => {
    const summary = summarizeRegionOutage(
      [
        { uid: "1", name: "API", regions: ["lauterbourg"] },
        { uid: "2", slug: "web", regions: ["lauterbourg", "paris"] },
        { uid: "3", name: "Fine", regions: ["paris"] },
      ],
      regions,
    );
    expect(summary?.blind.map((c) => c.name)).toEqual(["API"]);
    expect(summary?.reduced.map((c) => c.name)).toEqual(["web"]);
    expect(summary?.offline.map((r) => r.slug)).toEqual(["lauterbourg"]);
  });

  it("is null when nothing listed is affected", () => {
    expect(summarizeRegionOutage([{ uid: "3", regions: ["paris"] }], regions)).toBeNull();
  });
});

describe("offlineRegionNames", () => {
  it("renders emoji and name", () => {
    expect(offlineRegionNames(offlineRegions(regions))).toBe("🇫🇷 Lauterbourg, 🇩🇪 Frankfurt");
  });
});
