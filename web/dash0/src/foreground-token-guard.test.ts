import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync } from "node:fs";
import { join, relative } from "node:path";

// Regression guard for spec 2026-09-24-07: `text-destructive-foreground` was
// used 19 times with no matching `--destructive-foreground` /
// `--color-destructive-foreground` token, so Tailwind v4 generated NO CSS for
// it — worse than nothing, since tailwind-merge then dropped the working
// class it replaced. This scans every className-shaped string for a
// `<prefix>-<name>-foreground` utility and asserts the matching
// `--color-<name>-foreground` mapping exists in index.css's `@theme inline`
// block, so this bug class cannot come back under a different token name.

const SRC = __dirname;
const css = readFileSync(join(SRC, "index.css"), "utf8");
const themeInlineMatch = /@theme inline \{([\s\S]*?)\n\}/.exec(css);
if (!themeInlineMatch) throw new Error("no @theme inline block in index.css");
const themeInline = themeInlineMatch[1];

// Prefixes that read a Tailwind *color* (as opposed to e.g. `size-`, which
// happens to end in a word that could collide). Mirrors raw-blue-classes.test.ts.
const FOREGROUND_CLASS =
  /\b(?:text|bg|border(?:-[trblxyse])?|ring|fill|stroke)-([a-z][a-z0-9]*(?:-[a-z0-9]+)*)-foreground(?:\/\d+)?\b/g;

/** `<name>-foreground` hits (opacity suffix stripped) as `line: name-foreground` strings. */
function findForegroundClasses(source: string): string[] {
  const hits: string[] = [];
  source.split("\n").forEach((line, i) => {
    for (const m of line.matchAll(FOREGROUND_CLASS)) {
      hits.push(`${i + 1}: ${m[1]}-foreground`);
    }
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

/** Hits in `source` whose `--color-<name>-foreground` mapping is missing from `@theme inline`. */
function missingTokenOffenders(rel: string, source: string): string[] {
  const offenders: string[] = [];
  for (const hit of findForegroundClasses(source)) {
    const name = hit.split(": ")[1];
    if (!themeInline.includes(`--color-${name}: var(--${name});`)) {
      offenders.push(`${rel}:${hit}`);
    }
  }
  return offenders;
}

describe("<name>-foreground classes have a matching Tailwind color token", () => {
  it("positive control: the scanner catches the shape and strips an opacity suffix", () => {
    expect(findForegroundClasses('className="text-nope-foreground"')).toEqual([
      "1: nope-foreground",
    ]);
    expect(findForegroundClasses('className="bg-destructive-foreground/50"')).toEqual([
      "1: destructive-foreground",
    ]);
    // The bare base token is not a "<name>-foreground" class.
    expect(findForegroundClasses('className="text-foreground"')).toEqual([]);
  });

  it("positive control: a made-up text-nope-foreground class fails the guard", () => {
    expect(missingTokenOffenders("fixture.tsx", 'className="text-nope-foreground"')).toEqual([
      "fixture.tsx:1: nope-foreground",
    ]);
  });

  it("every <name>-foreground class used in src/ has a --color-<name>-foreground mapping", () => {
    const offenders: string[] = [];
    let scanned = 0;
    for (const file of sourceFiles(SRC)) {
      scanned++;
      offenders.push(...missingTokenOffenders(relative(SRC, file), readFileSync(file, "utf8")));
    }
    // Guard against a scanner that silently walks nothing.
    expect(scanned).toBeGreaterThan(100);
    expect(offenders).toEqual([]);
  });

  it("no call site hand-rolls the destructive confirm button classes anymore", () => {
    // Spec 2026-09-24-07 §3: every AlertDialogAction now goes through
    // variant="destructive" instead of a literal className, so the contrast
    // fix lives in one place (button.tsx) and can't drift per call site.
    // (The exact hand-rolled string, not just "bg-destructive
    // text-destructive-foreground" — that 2-class prefix legitimately
    // recurs inside button.tsx's own destructive variant definition.)
    const offenders: string[] = [];
    for (const file of sourceFiles(SRC)) {
      const source = readFileSync(file, "utf8");
      if (source.includes("bg-destructive text-destructive-foreground hover:bg-destructive/90")) {
        offenders.push(relative(SRC, file));
      }
    }
    expect(offenders).toEqual([]);
  });
});
