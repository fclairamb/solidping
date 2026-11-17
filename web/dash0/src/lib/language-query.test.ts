import { describe, expect, it } from "vitest";

import { parseLanguageQuery } from "@/lib/language-query";

describe("parseLanguageQuery", () => {
  it("leaves a URL without the parameter alone", () => {
    expect(parseLanguageQuery("https://acme.com/d/orgs/acme/checks?q=api")).toEqual({
      language: null,
      cleanedUrl: null,
    });
  });

  it("reads a supported language and drops the parameter, keeping the rest", () => {
    expect(
      parseLanguageQuery("https://acme.com/d/orgs/acme/checks?q=api&lang=fr#top"),
    ).toEqual({ language: "fr", cleanedUrl: "/d/orgs/acme/checks?q=api#top" });
  });

  it("maps a regional tag to its language", () => {
    expect(parseLanguageQuery("https://acme.com/d/?lang=de-AT").language).toBe("de");
  });

  it("drops an unknown language without picking it", () => {
    expect(parseLanguageQuery("https://acme.com/d/login?lang=xx")).toEqual({
      language: null,
      cleanedUrl: "/d/login",
    });
  });
});
