import { afterEach, describe, expect, it } from "vitest";
import i18n, { localeLoaders } from "./i18n";
import frCommon from "./locales/fr/common.json";

describe("lazy locales", () => {
  afterEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("changeLanguage('fr') resolves with French strings available", async () => {
    await i18n.changeLanguage("fr");
    expect(i18n.language).toBe("fr");
    expect(i18n.getResourceBundle("fr", "common")).toEqual(frCommon);
  });

  it("falls back to English for an unknown language", async () => {
    await i18n.changeLanguage("xx");
    expect(i18n.t("actions.save", { ns: "common", defaultValue: "" })).toBe(
      i18n.getFixedT("en", "common")("actions.save", { defaultValue: "" }),
    );
  });

  it("keeps the current language when the chunk fails to load", async () => {
    const original = localeLoaders.de;
    localeLoaders.de = () => Promise.reject(new Error("chunk failed"));
    try {
      await expect(i18n.changeLanguage("de")).rejects.toThrow("chunk failed");
      expect(i18n.language).toBe("en");
    } finally {
      localeLoaders.de = original;
    }
  });
});
