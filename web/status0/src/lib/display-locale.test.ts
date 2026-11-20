import { describe, test, expect } from "bun:test";
import { displayLocale } from "./display-locale";

describe("displayLocale", () => {
  test.each(["123", "en_", "a-b-c", ""])(
    "returns a tag Intl accepts for the invalid language %p",
    (lang) => {
      const tag = displayLocale({ resolvedLanguage: lang, language: lang });
      expect(tag).toBe("en");
      expect(() => new Intl.RelativeTimeFormat(tag)).not.toThrow();
      expect(() => new Date(0).toLocaleDateString(tag)).not.toThrow();
    },
  );

  test("keeps a valid primary language", () => {
    expect(displayLocale({ resolvedLanguage: "fr", language: "fr" })).toBe("fr");
  });

  test("reduces a regional tag to its primary language", () => {
    expect(displayLocale({ resolvedLanguage: "fr", language: "fr-CA" })).toBe("fr");
    expect(displayLocale({ language: "fr-CA" })).toBe("fr");
  });

  test("falls back to the language when the resolved one is missing", () => {
    expect(displayLocale({ language: "de" })).toBe("de");
  });

  test("ignores an invalid resolved language and uses a valid one", () => {
    expect(displayLocale({ resolvedLanguage: "123", language: "es" })).toBe("es");
  });

  test("returns en when nothing is set", () => {
    expect(displayLocale({})).toBe("en");
  });
});
