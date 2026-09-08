import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, basename } from "node:path";
import { fileURLToPath } from "node:url";

/**
 * The blind spot `locale-parity.test.ts` cannot cover.
 *
 * i18next lets a call carry an inline English fallback:
 *
 *     t("privateLocations.regions.title", "Private locations")
 *
 * That reads as internationalized and survives review, but if the key exists in
 * NO locale file, i18next renders the fallback — English, in every language.
 * And because the key is missing from `en`, `fr`, `de` and `es` *equally*, key
 * parity holds: the parity test is structurally incapable of noticing.
 *
 * That is not hypothetical. The whole "private locations" feature (83 strings)
 * shipped this way, and 46 more were scattered across integrations, server,
 * ssl-chain-card and docker-restart-loop-card — all of them invisible to every
 * check in the suite until this test existed.
 *
 * So: every `t("key", "default")` call in the app must resolve to a real key in
 * the `en` bundle. The inline default stays welcome as a last-resort fallback;
 * it just may not be the ONLY place the string lives.
 */

const HERE = fileURLToPath(new URL(".", import.meta.url));
const SRC = join(HERE, "..");
const EN = join(HERE, "en");

/**
 * `design-reference.tsx` is the internal component catalog. It renders code
 * SAMPLES inside template literals — text that looks like a `t()` call but is
 * documentation, not a call the app ever makes.
 */
const EXCLUDED_FILES = new Set(["design-reference.tsx"]);

type Json = { [k: string]: string | Json };

function loadBundles(): Record<string, Json> {
  const out: Record<string, Json> = {};
  for (const f of readdirSync(EN)) {
    if (f.endsWith(".json")) {
      out[f.slice(0, -5)] = JSON.parse(readFileSync(join(EN, f), "utf8")) as Json;
    }
  }
  return out;
}

function resolves(bundle: Json | undefined, key: string): boolean {
  let node: string | Json | undefined = bundle;
  for (const part of key.split(".")) {
    if (typeof node !== "object" || node === null || !(part in node)) return false;
    node = node[part];
  }
  return typeof node === "string" && node.length > 0;
}

function sourceFiles(dir: string, acc: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const p = join(dir, entry);
    if (statSync(p).isDirectory()) {
      if (entry !== "locales" && entry !== "node_modules") sourceFiles(p, acc);
    } else if (
      (entry.endsWith(".ts") || entry.endsWith(".tsx")) &&
      !entry.includes(".test.") &&
      !EXCLUDED_FILES.has(basename(entry))
    ) {
      acc.push(p);
    }
  }
  return acc;
}

// t("some.key", "An English default") — tolerant of newlines between the args,
// because prettier wraps the long ones.
const CALL = /\bt\(\s*"([A-Za-z0-9_.:]+)"\s*,\s*"([^"\\]{3,}?)"/gs;
// The namespace a bare key resolves against: useTranslation("ns") or
// useTranslation(["ns", …]), whose FIRST entry is the default namespace.
const NS = /useTranslation\(\s*\[?\s*"([a-zA-Z]+)"/;

describe("t() calls with an inline English default", () => {
  const bundles = loadBundles();
  const files = sourceFiles(SRC);

  it("scans a plausible number of source files", () => {
    // Guards the guard: a broken walk that finds nothing would make every
    // assertion below pass vacuously.
    expect(files.length).toBeGreaterThan(100);
  });

  it("every inline-default key exists in the en bundle", () => {
    const orphans: string[] = [];

    for (const file of files) {
      const src = readFileSync(file, "utf8");
      const defaultNs = NS.exec(src)?.[1];

      for (const [, key, english] of src.matchAll(CALL)) {
        const [ns, bare] = key.includes(":")
          ? (key.split(":", 2) as [string, string])
          : [defaultNs, key];
        if (!ns) continue;
        if (!resolves(bundles[ns], bare)) {
          orphans.push(
            `${file.slice(SRC.length + 1)}: t("${key}") → "${english}" is not in en/${ns}.json`,
          );
        }
      }
    }

    expect(
      orphans,
      `These strings render their English fallback in EVERY language. Add the key to all four locales.\n\n${orphans.join("\n")}\n`,
    ).toEqual([]);
  });
});
