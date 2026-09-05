import { describe, expect, it } from "vitest";

import authDe from "@/locales/de/auth.json";
import authEn from "@/locales/en/auth.json";
import authEs from "@/locales/es/auth.json";
import authFr from "@/locales/fr/auth.json";

// /no-org is the very first screen a brand-new account sees, and after spec
// 2026-09-05-01 it is also the screen that PROPOSES an organization ("Alice's
// organization") and demotes "join an existing one" to a disclosure. A missing
// key there renders the raw dotted path as the pre-filled organization NAME —
// i.e. the user would create an org literally called
// "auth:createOrg.suggestedPersonal". So all four locales must carry every key,
// as real non-empty strings.

const AUTH_LOCALES = [
  ["en", authEn],
  ["fr", authFr],
  ["de", authDe],
  ["es", authEs],
] as const;

const FRESH_ACCOUNT_KEYS = [
  "noOrg.welcome",
  "noOrg.subtitle",
  "noOrg.joinTitle",
  "noOrg.joinDescription",
  "noOrg.joinExpand",
  "noOrg.joinCollapse",
  "noOrg.joinSlugPlaceholder",
  "createOrg.title",
  "createOrg.description",
  "createOrg.orgName",
  "createOrg.suggestedPersonal",
  "createOrg.slugPreview",
  "createOrg.advanced",
  "createOrg.submit",
];

// Keys are looked up by walking segments, the way i18next resolves a dotted
// path.
function lookup(bundle: unknown, path: string): unknown {
  return path
    .split(".")
    .reduce<unknown>(
      (node, segment) =>
        node && typeof node === "object"
          ? (node as Record<string, unknown>)[segment]
          : undefined,
      bundle,
    );
}

describe("fresh-account /no-org copy locale parity", () => {
  it.each(AUTH_LOCALES)("%s carries every fresh-account key", (_locale, bundle) => {
    for (const key of FRESH_ACCOUNT_KEYS) {
      const value = lookup(bundle, key);
      expect(value, `${key} is missing`).toBeTypeOf("string");
      expect(String(value).trim(), `${key} is empty`).not.toBe("");
    }
  });

  // The possessive form is built by the LOCALE, never by string concatenation
  // in the component — "Alice's organization" and "L'organisation d'Alice" put
  // the name in different places. Every bundle must therefore interpolate.
  it("interpolates the first name in every locale's possessive form", () => {
    for (const [locale, bundle] of AUTH_LOCALES) {
      expect(
        String(lookup(bundle, "createOrg.suggestedPersonal")),
        `${locale} does not interpolate {{firstName}}`,
      ).toContain("{{firstName}}");
    }
  });

  // The proposal is submitted as the org NAME and the server derives the slug
  // from it, so a locale that wrapped the name in punctuation-only decoration
  // would be fine, but one that left nothing but the placeholder would produce
  // an org named after the person alone. Cheap guard: there must be real words
  // around the interpolation.
  it("keeps a real phrase around the interpolated name", () => {
    for (const [locale, bundle] of AUTH_LOCALES) {
      const phrase = String(lookup(bundle, "createOrg.suggestedPersonal"))
        .replace("{{firstName}}", "")
        .trim();
      expect(phrase.length, `${locale} is nothing but the placeholder`).toBeGreaterThan(2);
    }
  });

  it("translates rather than copying English into every locale", () => {
    for (const key of ["noOrg.subtitle", "noOrg.joinExpand", "createOrg.suggestedPersonal"]) {
      const english = String(lookup(authEn, key));
      for (const [locale, bundle] of AUTH_LOCALES) {
        if (locale === "en") continue;
        expect(
          String(lookup(bundle, key)),
          `${locale} still carries the English string for ${key}`,
        ).not.toBe(english);
      }
    }
  });

  // The join card must not hint at any particular org — least of all the
  // platform default, which is exactly what this spec stopped steering people
  // toward. The placeholder stays the generic example slug everywhere.
  it("keeps the join placeholder generic in every locale", () => {
    for (const [locale, bundle] of AUTH_LOCALES) {
      expect(
        lookup(bundle, "noOrg.joinSlugPlaceholder"),
        `${locale} hints at a real org slug`,
      ).toBe("acme");
    }
  });
});
