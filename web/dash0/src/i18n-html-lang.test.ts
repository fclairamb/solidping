/**
 * @vitest-environment jsdom
 */
import { afterEach, describe, expect, it } from "vitest";
import i18n, { i18nReady } from "./i18n";

// The static index.html says lang="en". When the app renders another
// language, a stale lang makes the browser offer to auto-translate a page
// that is already in the visitor's language, and the translator's DOM edits
// then crash React reconciliation (removeChild NotFoundError).
describe("<html lang> follows the rendered language", () => {
  afterEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("is the rendered language after a change", async () => {
    await i18nReady;
    await i18n.changeLanguage("es");
    expect(document.documentElement.lang).toBe("es");

    await i18n.changeLanguage("en");
    expect(document.documentElement.lang).toBe("en");
  });

  it("uses the base language for a regional variant", async () => {
    await i18n.changeLanguage("fr-CA");
    expect(document.documentElement.lang).toBe("fr");
  });

  it("stays English for a language the app does not ship", async () => {
    await i18n.changeLanguage("pt-BR");
    expect(document.documentElement.lang).toBe("en");
  });
});
