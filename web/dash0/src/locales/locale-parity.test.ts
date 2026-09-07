import { describe, expect, it } from "vitest";
import fs from "node:fs";
import path from "node:path";

// Generalizes lib/operator-notifications-locales.test.ts (which covered one
// namespace) to the whole locales/ tree (spec
// 2026-09-07-02-untranslated-strings-and-demo-refusal-message §C). The bug
// class this guards against is a component that ships with `t()` calls whose
// keys only exist in `en` — it renders fine in English and silently falls
// back to raw English strings (or the raw dotted key, with no fallback) in
// every other locale. Comparing KEY SETS across locales — not values — is
// what catches "this namespace file was never updated for fr/de/es" without
// having to know what any individual page renders.
//
// This test is expected to fail the day a new namespace or key is added to
// `en` but not mirrored elsewhere — that failure IS the point: fix the
// locale file, don't narrow this test.

const LOCALES_DIR = path.resolve(__dirname);
const LANGS = ["en", "fr", "de", "es"] as const;

/** Recursively lists every leaf key path, dotted, the way i18next resolves a
 * nested key ("form.confirmationPeriod"). Array values are treated as leaves
 * (a locale file has none today, but arrays should be compared for equality
 * of the array itself, not key-recursed). */
function flattenKeys(obj: unknown, prefix = ""): string[] {
  if (obj === null || typeof obj !== "object" || Array.isArray(obj)) {
    return prefix ? [prefix] : [];
  }
  const keys: string[] = [];
  for (const [key, value] of Object.entries(obj as Record<string, unknown>)) {
    const nextPrefix = prefix ? `${prefix}.${key}` : key;
    if (value !== null && typeof value === "object" && !Array.isArray(value)) {
      keys.push(...flattenKeys(value, nextPrefix));
    } else {
      keys.push(nextPrefix);
    }
  }
  return keys;
}

function readJson(filePath: string): unknown {
  return JSON.parse(fs.readFileSync(filePath, "utf-8"));
}

const namespaces = fs
  .readdirSync(path.join(LOCALES_DIR, "en"))
  .filter((f) => f.endsWith(".json"))
  .map((f) => f.replace(/\.json$/, ""))
  .sort();

describe("locale parity — every namespace carries the same key set in every locale", () => {
  it("found at least one namespace to check (sanity check on the test itself)", () => {
    expect(namespaces.length).toBeGreaterThan(0);
  });

  for (const ns of namespaces) {
    const enPath = path.join(LOCALES_DIR, "en", `${ns}.json`);
    const enKeys = new Set(flattenKeys(readJson(enPath)));

    it.each(LANGS.filter((lang) => lang !== "en"))(
      `${ns}.json: %s has the same keys as en`,
      (lang) => {
        const bundlePath = path.join(LOCALES_DIR, lang, `${ns}.json`);
        expect(
          fs.existsSync(bundlePath),
          `${lang}/${ns}.json does not exist`,
        ).toBe(true);

        const keys = new Set(flattenKeys(readJson(bundlePath)));

        const missing = [...enKeys].filter((k) => !keys.has(k)).sort();
        const extra = [...keys].filter((k) => !enKeys.has(k)).sort();

        expect(
          missing,
          `${lang}/${ns}.json is missing keys present in en: ${missing.join(", ")}`,
        ).toEqual([]);
        expect(
          extra,
          `${lang}/${ns}.json has keys not present in en (stale or renamed): ${extra.join(", ")}`,
        ).toEqual([]);
      },
    );
  }
});
