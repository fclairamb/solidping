import { describe, expect, it } from "vitest";

import { crawlModule, durationToMinutes } from "./crawl";

describe("crawlModule", () => {
  it("writes only what departs from the defaults", () => {
    const state = crawlModule.fromConfig({ url: "https://www.acme.com/" });
    const { config, errors } = crawlModule.toConfig(state);

    expect(errors).toEqual([]);
    expect(config).toEqual({ url: "https://www.acme.com/" });
  });

  it("round-trips a fully customized config", () => {
    const stored = {
      url: "https://www.acme.com/",
      maxPages: 500,
      checkExternalLinks: false,
      checkMixedContent: false,
      respectRobots: false,
      sitemap: "https://www.acme.com/map.xml",
      include: ["/blog"],
      exclude: ["/admin", "/*/print"],
      concurrency: 3,
      delayMs: 0,
      maxRunDuration: "45m",
      failOn: ["broken_link", "broken_external_link"],
    };

    const { config, errors } = crawlModule.toConfig(crawlModule.fromConfig(stored));

    expect(errors).toEqual([]);
    expect(config).toEqual(stored);
    for (const key of Object.keys(config)) {
      expect(crawlModule.ownedKeys).toContain(key);
    }
  });

  it("requires a URL and bounds maxPages and the run duration", () => {
    const state = {
      ...crawlModule.fromConfig({}),
      maxPages: "2001",
      maxRunMinutes: "3",
    };
    const names = crawlModule.toConfig(state).errors.map((e) => e.name);

    expect(names).toEqual(["url", "maxPages", "maxRunDuration"]);
  });

  it("keeps sitemap off explicit", () => {
    const state = { ...crawlModule.fromConfig({ url: "https://www.acme.com/" }), sitemapMode: "off" as const };

    expect(crawlModule.toConfig(state).config.sitemap).toBe("off");
  });
});

describe("durationToMinutes", () => {
  it("reads Go durations", () => {
    expect(durationToMinutes("30m")).toBe("30");
    expect(durationToMinutes("1h30m0s")).toBe("90");
    expect(durationToMinutes("2h")).toBe("120");
    expect(durationToMinutes("")).toBe("");
    expect(durationToMinutes("soon")).toBe("");
  });
});
