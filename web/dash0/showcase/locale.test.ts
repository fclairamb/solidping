import { describe, expect, it } from "vitest";

import {
  SHOWCASE_LOCALES,
  browserLocaleTag,
  localeSubdir,
  parseShowcaseLocale,
  showcaseContent,
} from "./locale";

describe("parseShowcaseLocale", () => {
  it("defaults to English", () => {
    expect(parseShowcaseLocale(undefined)).toBe("en");
    expect(parseShowcaseLocale("")).toBe("en");
  });

  it("accepts every dashboard language, case-insensitively", () => {
    expect(parseShowcaseLocale("fr")).toBe("fr");
    expect(parseShowcaseLocale(" DE ")).toBe("de");
  });

  it("refuses an unknown language rather than filming English", () => {
    expect(() => parseShowcaseLocale("it")).toThrow(/SHOWCASE_LOCALE/);
    expect(() => parseShowcaseLocale("fr-FR")).toThrow(/SHOWCASE_LOCALE/);
  });
});

describe("browserLocaleTag / localeSubdir", () => {
  it("maps to a region tag the dashboard resolves back to the same language", () => {
    for (const locale of SHOWCASE_LOCALES) {
      expect(browserLocaleTag(locale).split("-")[0]).toBe(locale);
    }
  });

  it("keeps English stills at the flat, already-published paths", () => {
    expect(localeSubdir("en")).toBe("");
    expect(localeSubdir("fr")).toBe("fr");
  });
});

describe("showcaseContent", () => {
  it("keeps the English content the published cut was filmed with", () => {
    const en = showcaseContent("en");
    expect(en.demoChecks.map((c) => c.name)).toEqual([
      "Marketing site",
      "Docs site",
      "Checkout API",
    ]);
    expect(en.featured).toEqual({
      name: "Production API",
      url: "https://solidping.io/api/mgmt/health",
    });
    expect(en.userName).toBe("Alex Rivera");
  });

  it("localizes names but never URLs", () => {
    const en = showcaseContent("en");
    for (const locale of SHOWCASE_LOCALES.filter((l) => l !== "en")) {
      const c = showcaseContent(locale);
      expect(c.demoChecks.map((d) => d.config.url)).toEqual(
        en.demoChecks.map((d) => d.config.url),
      );
      expect(c.featured.url).toBe(en.featured.url);
      expect(c.featured.name).not.toBe(en.featured.name);
      expect(new Set(c.demoChecks.map((d) => d.name)).size).toBe(3);
    }
  });
});
