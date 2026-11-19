import { afterEach, describe, expect, it } from "vitest";
import i18n from "@/i18n";
import { asTranslateFn } from "@/lib/i18n-types";
import { cleanTranslations, localizeGenerated } from "@/lib/localized-text";

const t = asTranslateFn((key, options) => i18n.t(key, options) as string);

describe("localizeGenerated", () => {
  afterEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("renders a generated incident title in the reader's language", async () => {
    await i18n.changeLanguage("de");
    expect(
      localizeGenerated(
        t,
        { key: "incident.title.down", params: { name: "database" } },
        "database is down",
      ),
    ).toBe("database ist ausgefallen");
  });

  it("formats numbers in the reader's locale", async () => {
    await i18n.changeLanguage("fr");
    expect(
      localizeGenerated(
        t,
        {
          key: "incident.title.degradedFailuresAvailability",
          params: { name: "api", failures: 3, probes: 10, availability: 70 },
        },
        "literal",
      ),
    ).toBe(
      "api est dégradé : 3 échecs sur les 10 dernières sondes (70,0 % de disponibilité)",
    );
  });

  it("renders an auto-published status-page entry, naming the group when unnamed", async () => {
    await i18n.changeLanguage("fr");
    expect(
      localizeGenerated(
        t,
        { key: "publication.openedTitle", params: { name: "Database" } },
        "Database is experiencing issues",
      ),
    ).toBe("Database rencontre des difficultés");
    expect(
      localizeGenerated(t, { key: "publication.groupOpenedTitle" }, "literal"),
    ).toBe("Certains services rencontrent des difficultés");
  });

  it("keeps the stored text without a recipe or with an unknown key", () => {
    expect(localizeGenerated(t, undefined, "Written by hand")).toBe(
      "Written by hand",
    );
    expect(
      localizeGenerated(t, { key: "incident.title.fromTheFuture" }, "literal"),
    ).toBe("literal");
  });
});

describe("localizeGenerated with hand-written translations", () => {
  const recipe = { key: "publication.openedTitle", params: { name: "Database" } };
  const translations = { fr: { title: "Panne de la base", body: "Nous enquêtons." } };

  afterEach(async () => {
    await i18n.changeLanguage("en");
  });

  it("a translation wins over the recipe", async () => {
    await i18n.changeLanguage("fr");
    expect(
      localizeGenerated(t, recipe, "Database is experiencing issues", {
        translations,
        field: "title",
      }),
    ).toBe("Panne de la base");
    expect(
      localizeGenerated(t, undefined, "We are looking", { translations, field: "body" }),
    ).toBe("Nous enquêtons.");
  });

  it("the recipe wins over the literal when the language has no entry", async () => {
    await i18n.changeLanguage("de");
    expect(
      localizeGenerated(t, recipe, "Database is experiencing issues", {
        translations,
        field: "title",
      }),
    ).toBe(
      localizeGenerated(t, recipe, "Database is experiencing issues"),
    );
    expect(
      localizeGenerated(t, recipe, "Database is experiencing issues", {
        translations,
        field: "title",
      }),
    ).not.toBe("Database is experiencing issues");
  });

  it("falls through to the literal text without a recipe or an entry", async () => {
    await i18n.changeLanguage("de");
    expect(
      localizeGenerated(t, undefined, "Written by hand", { translations, field: "title" }),
    ).toBe("Written by hand");
  });

  it("matches a regional tag on its primary subtag", () => {
    expect(
      localizeGenerated(t, undefined, "literal", {
        translations,
        field: "title",
        language: "fr-CA",
      }),
    ).toBe("Panne de la base");
  });
});

describe("cleanTranslations", () => {
  it("drops empty groups and keeps half-filled ones", () => {
    expect(
      cleanTranslations({
        fr: { title: "Titre", body: "Corps" },
        de: { title: "", body: "" },
        es: { title: "Solo título" },
      }),
    ).toEqual({ fr: { title: "Titre", body: "Corps" }, es: { title: "Solo título" } });
  });
});
