import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";
import {
  DASH0_IDENTICAL_ALLOWLIST,
  STATUS0_IDENTICAL_ALLOWLIST,
  type AllowlistLang,
  type IdenticalAllowlist,
} from "./identical-allowlist";

// The blind spot of `locale-parity.test.ts` (spec 2026-09-26-01).
//
// Key parity holds when a locale file was seeded by copying `en` and never
// translated: every key is there, it just says the English thing. That is how
// the French login page came to read "Sign in with passkey" (the whole 2FA
// step, and the passkey / TOTP account settings, were English in fr, de and
// es), with every test green.
//
// So this test compares VALUES: a non-`en` leaf identical to the `en` leaf
// fails, unless `identical-allowlist.ts` names that key for that locale. It
// also checks that each translation keeps the `en` string's interpolation
// placeholders and Trans tags, which a hand translation can silently drop.
//
// It covers dash0 and status0. status0 has no unit-test runner of its own, so
// its locale tree is checked from here.

const LANGS: readonly AllowlistLang[] = ["fr", "de", "es"];

type Json = { [k: string]: string | Json };

const TREES: readonly { app: string; dir: string; allowlist: IdenticalAllowlist }[] = [
  { app: "dash0", dir: path.resolve(__dirname), allowlist: DASH0_IDENTICAL_ALLOWLIST },
  {
    app: "status0",
    dir: path.resolve(__dirname, "../../../status0/src/locales"),
    allowlist: STATUS0_IDENTICAL_ALLOWLIST,
  },
];

/** Every leaf as [dotted key path, value]. */
function flattenLeaves(obj: Json, prefix = ""): [string, unknown][] {
  const out: [string, unknown][] = [];
  for (const [key, value] of Object.entries(obj)) {
    const next = prefix ? `${prefix}.${key}` : key;
    if (value !== null && typeof value === "object" && !Array.isArray(value)) {
      out.push(...flattenLeaves(value, next));
    } else {
      out.push([next, value]);
    }
  }
  return out;
}

/** A value with no letters outside `{{placeholders}}` ("—", "09:00",
 * "{{from}} → {{to}}") has nothing to translate. */
function hasTranslatableText(value: string): boolean {
  return /\p{L}/u.test(value.replace(/\{\{[^}]*\}\}/g, ""));
}

/**
 * Key paths (`<ns>:<key>`) whose `lang` value is identical to `en` and not
 * allowlisted for `lang`. Pure, so the positive controls below can feed it
 * fixtures.
 */
function findUntranslated(
  ns: string,
  en: Json,
  other: Json,
  lang: AllowlistLang,
  allowlist: IdenticalAllowlist,
): string[] {
  const otherLeaves = new Map(flattenLeaves(other));
  const found: string[] = [];
  for (const [key, enValue] of flattenLeaves(en)) {
    if (typeof enValue !== "string" || !hasTranslatableText(enValue)) continue;
    if (otherLeaves.get(key) !== enValue) continue;
    const id = `${ns}:${key}`;
    if (allowlist[id]?.includes(lang)) continue;
    found.push(id);
  }
  return found;
}

/** Allowlist entries for `lang` that no longer describe an identical value. */
function findStaleAllowlistEntries(
  bundles: Map<string, { en: Json; langs: Record<AllowlistLang, Json> }>,
  lang: AllowlistLang,
  allowlist: IdenticalAllowlist,
): string[] {
  const stale: string[] = [];
  for (const [id, langs] of Object.entries(allowlist)) {
    if (!langs.includes(lang)) continue;
    const [ns, key] = [id.slice(0, id.indexOf(":")), id.slice(id.indexOf(":") + 1)];
    const bundle = bundles.get(ns);
    const enValue = bundle && new Map(flattenLeaves(bundle.en)).get(key);
    const value = bundle && new Map(flattenLeaves(bundle.langs[lang])).get(key);
    if (typeof enValue !== "string" || value !== enValue) stale.push(id);
  }
  return stale;
}

/** The `{{placeholder}}` and Trans `<tag>` tokens of a string, sorted and
 * de-duplicated so word order may change in translation. */
function tokens(value: string): string[] {
  const found = [
    ...(value.match(/\{\{\s*[^}]+?\s*\}\}/g) ?? []).map((t) => t.replace(/\s+/g, "")),
    ...(value.match(/<\/?[A-Za-z0-9_]+\s*\/?>/g) ?? []),
  ];
  return [...new Set(found)].sort();
}

function findTokenMismatches(ns: string, en: Json, other: Json): string[] {
  const otherLeaves = new Map(flattenLeaves(other));
  const out: string[] = [];
  for (const [key, enValue] of flattenLeaves(en)) {
    const value = otherLeaves.get(key);
    if (typeof enValue !== "string" || typeof value !== "string") continue;
    const want = tokens(enValue);
    const got = tokens(value);
    if (want.join("|") !== got.join("|")) {
      out.push(`${ns}:${key} (en ${JSON.stringify(want)}, got ${JSON.stringify(got)})`);
    }
  }
  return out;
}

function readJson(file: string): Json {
  return JSON.parse(fs.readFileSync(file, "utf-8")) as Json;
}

function loadTree(dir: string) {
  const bundles = new Map<string, { en: Json; langs: Record<AllowlistLang, Json> }>();
  for (const file of fs.readdirSync(path.join(dir, "en")).filter((f) => f.endsWith(".json")).sort()) {
    const ns = file.replace(/\.json$/, "");
    bundles.set(ns, {
      en: readJson(path.join(dir, "en", file)),
      langs: {
        fr: readJson(path.join(dir, "fr", file)),
        de: readJson(path.join(dir, "de", file)),
        es: readJson(path.join(dir, "es", file)),
      },
    });
  }
  return bundles;
}

for (const tree of TREES) {
  const bundles = loadTree(tree.dir);

  describe(`${tree.app} locales — no value left in English`, () => {
    it("found namespaces to check (sanity check on the test itself)", () => {
      expect(bundles.size).toBeGreaterThan(0);
    });

    for (const [ns, bundle] of bundles) {
      it.each(LANGS)(`${ns}.json: %s has no value identical to en outside the allowlist`, (lang) => {
        const untranslated = findUntranslated(ns, bundle.en, bundle.langs[lang], lang, tree.allowlist);
        expect(
          untranslated,
          `${tree.app} ${lang}/${ns}.json still carries the English text for: ${untranslated.join(", ")}. ` +
            `Translate them. Only a value that is correct as-is (brand, protocol, sample value, same word) ` +
            `goes in identical-allowlist.ts.`,
        ).toEqual([]);
      });

      it.each(LANGS)(`${ns}.json: %s keeps en's {{placeholders}} and Trans tags`, (lang) => {
        const mismatches = findTokenMismatches(ns, bundle.en, bundle.langs[lang]);
        expect(mismatches, `${tree.app} ${lang}/${ns}.json: ${mismatches.join("; ")}`).toEqual([]);
      });
    }

    it.each(LANGS)("identical-allowlist.ts: every %s entry still matches an identical value", (lang) => {
      const stale = findStaleAllowlistEntries(bundles, lang, tree.allowlist);
      expect(
        stale,
        `${tree.app} allowlist entries for ${lang} that are translated now or no longer exist: ${stale.join(", ")}. ` +
          `Remove them (or drop "${lang}" from their locale list).`,
      ).toEqual([]);
    });
  });
}

// Positive controls: the guard must be able to fail. Without these, a bug in
// flattenLeaves or the comparison could turn every assertion above into a
// vacuous pass.
describe("untranslated-values guard — positive controls", () => {
  const en: Json = {
    twoFactor: { signInWithPasskey: "Sign in with passkey", verify: "Verify" },
    brand: "GitHub",
    dash: "—",
    range: "{{from}} → {{to}}",
    count: "{{count}} checks",
  };

  it("flags a value copied verbatim from en", () => {
    const fr: Json = {
      twoFactor: { signInWithPasskey: "Sign in with passkey", verify: "Vérifier" },
      brand: "GitHub",
      dash: "—",
      range: "{{from}} → {{to}}",
      count: "{{count}} vérifications",
    };
    expect(findUntranslated("auth", en, fr, "fr", {})).toEqual([
      "auth:twoFactor.signInWithPasskey",
      "auth:brand",
    ]);
  });

  it("honors the allowlist per locale, not per key", () => {
    const same: Json = { ...en };
    const allowlist: IdenticalAllowlist = {
      "auth:brand": ["fr", "de", "es"],
      "auth:twoFactor.signInWithPasskey": ["de"],
      "auth:twoFactor.verify": ["de"],
      "auth:count": ["de"],
    };
    expect(findUntranslated("auth", en, same, "de", allowlist)).toEqual([]);
    expect(findUntranslated("auth", en, same, "fr", allowlist)).toEqual([
      "auth:twoFactor.signInWithPasskey",
      "auth:twoFactor.verify",
      "auth:count",
    ]);
  });

  it("reports an allowlist entry whose value got translated", () => {
    const bundles = new Map([
      ["auth", { en, langs: { fr: { ...en, brand: "GitHub (fr)" }, de: en, es: en } }],
    ]);
    expect(
      findStaleAllowlistEntries(bundles, "fr", { "auth:brand": ["fr"], "auth:gone": ["fr"] }),
    ).toEqual(["auth:brand", "auth:gone"]);
  });

  it("flags a translation that dropped or renamed a placeholder or tag", () => {
    const src: Json = { a: "{{count}} checks in {{region}}", b: "Watch <accent>closely</accent>" };
    const bad: Json = { a: "{{count}} vérifications", b: "Surveillez de près" };
    const good: Json = { a: "{{count}} vérifications dans {{ region }}", b: "Surveillez <accent>de près</accent>" };
    expect(findTokenMismatches("x", src, bad)).toHaveLength(2);
    expect(findTokenMismatches("x", src, good)).toEqual([]);
  });
});
