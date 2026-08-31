import { beforeAll, describe, expect, it } from "vitest";
import i18next, { type TFunction } from "i18next";

import depsEn from "@/locales/en/dependencies.json";
import depsFr from "@/locales/fr/dependencies.json";
import depsDe from "@/locales/de/dependencies.json";
import depsEs from "@/locales/es/dependencies.json";

// The confirmation-margin lint banner (spec 2026-08-31-06) is the only place
// the numbers behind the hold are ever explained to an operator, so a locale
// that silently falls back to the raw key turns it into gibberish. These
// resolve against REAL i18next instances backed by the actual bundles, so a
// key added to en/ and forgotten anywhere else fails here rather than in
// production.
const bundles: Record<string, Record<string, unknown>> = {
  en: depsEn,
  fr: depsFr,
  de: depsDe,
  es: depsEs,
};

const KEYS = [
  "warnings.confirmationMargin.title",
  "warnings.confirmationMargin.body",
] as const;

const translators: Record<string, TFunction> = {};

beforeAll(async () => {
  for (const [lng, bundle] of Object.entries(bundles)) {
    const instance = i18next.createInstance();
    await instance.init({
      lng,
      resources: { [lng]: { dependencies: bundle } },
      interpolation: { escapeValue: false },
    });
    translators[lng] = instance.getFixedT(lng, "dependencies");
  }
});

describe("dependency warning copy", () => {
  it("renders the English banner with the parent and both durations", () => {
    const t = translators.en;
    const vars = { parent: "rabbitmq-aws", current: 120, recommended: 195 };

    expect(t("warnings.confirmationMargin.title", vars)).toContain("rabbitmq-aws");

    const body = t("warnings.confirmationMargin.body", vars);
    expect(body).toContain("rabbitmq-aws");
    expect(body).toContain("120");
    expect(body).toContain("195");
  });

  it.each(Object.keys(bundles))("has every key translated in %s", (lng) => {
    const t = translators[lng];

    for (const key of KEYS) {
      const value = t(key, { parent: "rabbitmq-aws", current: 120, recommended: 195 });

      // A missing key makes i18next echo the key itself back.
      expect(value).not.toBe(key);
      expect(value.trim().length).toBeGreaterThan(0);
    }
  });

  it.each(Object.keys(bundles))(
    "interpolates every placeholder in %s (no leftover {{…}})",
    (lng) => {
      const t = translators[lng];

      for (const key of KEYS) {
        const value = t(key, { parent: "rabbitmq-aws", current: 120, recommended: 195 });
        expect(value).not.toMatch(/\{\{/);
      }
    },
  );

  it.each(Object.keys(bundles))("names the parent in the %s body", (lng) => {
    const value = translators[lng]("warnings.confirmationMargin.body", {
      parent: "rabbitmq-aws",
      current: 120,
      recommended: 195,
    });

    expect(value).toContain("rabbitmq-aws");
    expect(value).toContain("120");
    expect(value).toContain("195");
  });
});
