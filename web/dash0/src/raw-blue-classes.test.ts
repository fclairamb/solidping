import { describe, expect, it } from "vitest";
import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";

// Spec 2026-09-24-01 §4: a raw Tailwind blue / sky / indigo class written
// against the old primary now sits next to a different blue. Anything that
// means "primary, info or interactive" uses the --primary token instead. The
// only raw blues left are CATEGORY colors, each allowlisted below with why.

const SRC = __dirname;

const RAW_BLUE =
  /\b(?:bg|text|border(?:-[trblxyse])?|ring|from|via|to|fill|stroke|outline|decoration|divide|shadow)-(?:blue|sky|indigo)-\d+/g;

const ALLOWLIST: Record<string, string> = {
  "components/shared/check-type-identity.tsx":
    "per-check-type family tones (http blue, databases indigo, infra sky) are categorical identities, pinned by check-type-identity.test.ts",
  "routes/orgs/$org/on-call.$uid.index.tsx":
    "per-participant calendar palette with text-white on it; dark --primary is a light blue white text cannot sit on",
};

/** Raw blue classes found in `source`, as `line: match` strings. */
function findRawBlue(source: string): string[] {
  const hits: string[] = [];
  source.split("\n").forEach((line, i) => {
    for (const m of line.matchAll(RAW_BLUE)) hits.push(`${i + 1}: ${m[0]}`);
  });
  return hits;
}

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...sourceFiles(path));
    } else if (/\.(ts|tsx)$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) {
      out.push(path);
    }
  }
  return out;
}

describe("raw blue / sky / indigo classes", () => {
  it("positive control: the scanner catches the shapes the triage removed", () => {
    expect(findRawBlue('const x = "text-blue-600 dark:text-blue-400";')).toEqual([
      "1: text-blue-600",
      "1: text-blue-400",
    ]);
    expect(findRawBlue('ok\nclassName="bg-sky-500/10 border border-sky-500/20"')).toEqual([
      "2: bg-sky-500",
      "2: border-sky-500",
    ]);
    expect(findRawBlue('"hover:ring-indigo-300 from-blue-500 border-l-blue-600"')).toHaveLength(3);
    // Not a class: prose and the token-based replacements stay clean.
    expect(findRawBlue("configuration blue (shipped); bg-primary/10 text-primary")).toEqual([]);
  });

  it("appear only in the allowlisted category-color files", () => {
    const offenders: string[] = [];
    let scanned = 0;
    for (const file of sourceFiles(SRC)) {
      scanned++;
      const rel = relative(SRC, file);
      if (rel in ALLOWLIST) continue;
      for (const hit of findRawBlue(readFileSync(file, "utf8"))) {
        offenders.push(`${rel}:${hit}`);
      }
    }
    // Guard against a scanner that silently walks nothing.
    expect(scanned).toBeGreaterThan(100);
    expect(offenders).toEqual([]);
  });

  it("every allowlisted file still exists and still needs its entry", () => {
    for (const rel of Object.keys(ALLOWLIST)) {
      const hits = findRawBlue(readFileSync(join(SRC, rel), "utf8"));
      expect(hits.length, `${rel} no longer has raw blues: drop it from the allowlist`).toBeGreaterThan(0);
    }
  });
});
