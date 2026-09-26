import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// Spec 2026-09-24-04: status0 carries dash0's electric-identity palette
// without a shared package, and without taking anything away from customer
// theming. These tests pin both halves at the source level; the rendered
// behaviour is covered by e2e/electric-identity.spec.ts.

const status0 = join(import.meta.dir, "..");
const repo = join(status0, "..", "..");

const read = (...parts: string[]) => readFileSync(join(...parts), "utf8");

const statusCss = read(status0, "src", "index.css");
const dashCss = read(repo, "web", "dash0", "src", "index.css");

/**
 * Every top-level rule of a stylesheet as [selector, body] pairs. Only the
 * shallow shape these two files use is supported: `@theme inline { … }` and
 * plain `selector { … }` rules with no nested braces except `@utility`/
 * `@layer`, which carry no custom-property declarations of interest.
 */
function topLevelRules(css: string): Array<[string, string]> {
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const rules: Array<[string, string]> = [];
  let depth = 0;
  let start = 0;
  let selector = "";
  for (let i = 0; i < stripped.length; i++) {
    const ch = stripped[i];
    if (ch === "{") {
      if (depth === 0) {
        selector = stripped.slice(start, i).split(";").pop()!.trim();
        start = i + 1;
      }
      depth++;
    } else if (ch === "}") {
      depth--;
      if (depth === 0) {
        rules.push([selector, stripped.slice(start, i)]);
        start = i + 1;
      }
    }
  }
  return rules;
}

/** The custom properties declared directly in the rule for `selector`. */
function declarations(css: string, selector: string): Map<string, string> {
  const rule = topLevelRules(css).find(([sel]) => sel === selector);
  if (!rule) throw new Error(`no top-level "${selector}" rule`);
  const out = new Map<string, string>();
  for (const m of rule[1].matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
    out.set(m[1], m[2].replace(/\s+/g, " ").trim());
  }
  return out;
}

// Spec 01 §1 (neutrals, --primary, --ring, --chart-1, --chart-5) and §2
// (--primary-gradient, --gradient-foreground). --control is left out on
// purpose: nothing in status0 reads it.
const MIRRORED = [
  "--background",
  "--foreground",
  "--card",
  "--card-foreground",
  "--popover",
  "--popover-foreground",
  "--secondary",
  "--secondary-foreground",
  "--muted",
  "--muted-foreground",
  "--accent",
  "--accent-foreground",
  "--border",
  "--input",
  "--primary",
  "--primary-foreground",
  "--ring",
  "--chart-1",
  "--chart-5",
  "--primary-gradient",
  "--gradient-foreground",
];

// Every variable documented in web/docs/docs/features/status-pages.md.
const DOCUMENTED = [
  "--brand",
  "--brand-foreground",
  "--background",
  "--foreground",
  "--card",
  "--card-foreground",
  "--border",
  "--muted",
  "--muted-foreground",
  "--primary",
  "--primary-foreground",
  "--primary-gradient",
  "--gradient-foreground",
  "--status-ok",
  "--status-warning",
  "--status-error",
  "--radius",
];

describe("status0 tokens mirror dash0", () => {
  for (const selector of [":root", ".dark"]) {
    test(`${selector}: the electric-identity tokens equal dash0's`, () => {
      const mine = declarations(statusCss, selector);
      const theirs = declarations(dashCss, selector);
      for (const token of MIRRORED) {
        expect({ token, value: mine.get(token) }).toEqual({
          token,
          value: theirs.get(token),
        });
      }
    });
  }

  test("brand and status colors keep their pre-electric values", () => {
    const root = declarations(statusCss, ":root");
    const dark = declarations(statusCss, ".dark");
    // The logo stays crimson; status colors carry meaning, not identity.
    expect(root.get("--brand")).toBe("oklch(0.58 0.22 5)");
    expect(dark.get("--brand")).toBe("oklch(0.65 0.2 5)");
    expect(root.get("--status-ok")).toBe("oklch(0.65 0.2 145)");
    expect(root.get("--status-warning")).toBe("oklch(0.75 0.18 85)");
    expect(root.get("--status-error")).toBe("oklch(0.6 0.22 25)");
    expect(dark.get("--status-ok")).toBe("oklch(0.7 0.18 145)");
    expect(dark.get("--status-warning")).toBe("oklch(0.8 0.16 85)");
    expect(dark.get("--status-error")).toBe("oklch(0.65 0.2 25)");
  });

  test("no decorative SolidPing gradient on a customer's page", () => {
    for (const token of ["--page-glow", "--hero-gradient", "--accent-gradient"]) {
      expect(statusCss).not.toContain(token);
    }
    expect(statusCss).toContain("@utility bg-primary-gradient");
  });
});

describe("customer theming keeps precedence", () => {
  test("documented variables are declared only on plain :root / .dark", () => {
    // The operator <style> is rendered after index.css, so it wins on EQUAL
    // specificity. A token moved to `:root.dark`, `html.dark`, `body`… would
    // silently beat every customer stylesheet.
    const offenders: string[] = [];
    for (const [selector, body] of topLevelRules(statusCss)) {
      if (selector === ":root" || selector === ".dark") continue;
      if (selector.startsWith("@theme")) continue; // --color-* aliases only
      for (const token of DOCUMENTED) {
        if (new RegExp(`(^|[\\s;{])${token}\\s*:`).test(body)) {
          offenders.push(`${selector} declares ${token}`);
        }
      }
    }
    expect(offenders).toEqual([]);
    const code = statusCss.replace(/\/\*[\s\S]*?\*\//g, "");
    expect(code).not.toContain("!important");
  });

  test("the docs and the dash0 starter template list the button variables", () => {
    const docs = read(repo, "web", "docs", "docs", "features", "status-pages.md");
    const template = read(
      repo,
      "web",
      "dash0",
      "src",
      "routes",
      "orgs",
      "$org",
      "status-pages.$statusPageUid.appearance.tsx",
    );
    for (const token of [
      "--primary",
      "--primary-foreground",
      "--primary-gradient",
      "--gradient-foreground",
    ]) {
      expect(docs).toContain(`| \`${token}\` |`);
      expect(template).toContain(`${token}:`);
    }
    // The template's default neutrals are the electric ones.
    for (const hex of ["#f5f9fc", "#09121f", "#dee3eb", "#060a13", "#0c131e"]) {
      expect(template).toContain(hex);
    }
    for (const old of ["#f8fafc", "#0f172a", "#e2e8f0", "#0b1220", "#131c2e", "#24314a"]) {
      expect(template).not.toContain(old);
    }
    expect(template).toContain("--brand: #e11d63;");
  });
});

describe("no old-palette literals", () => {
  test("theme-color is the page background of each theme", () => {
    const html = read(status0, "index.html");
    const metas = [
      ...html.matchAll(
        /<meta\s+name="theme-color"\s+content="([^"]+)"\s+media="([^"]+)"/g,
      ),
    ].map((m) => [m[1], m[2]]);
    expect(metas).toEqual([
      ["#f5f9fc", "(prefers-color-scheme: light)"],
      ["#060a13", "(prefers-color-scheme: dark)"],
    ]);
    expect(html).not.toContain("#e91e63");
    expect(html).not.toContain("#0e0a0e");
  });

  test("the manifest uses the light page background", () => {
    const manifest = JSON.parse(
      read(status0, "public", "manifest.webmanifest"),
    ) as { theme_color: string; background_color: string };
    expect(manifest.theme_color).toBe("#f5f9fc");
    expect(manifest.background_color).toBe("#f5f9fc");
  });

  test("the TV shell reads the tokens instead of the old slate", () => {
    const tv = read(status0, "src", "components", "tv", "tv-route.tsx");
    expect(tv).not.toContain("oklch(0.19_0.01_250)");
    expect(tv).not.toContain("oklch(0.93_0.01_250)");
    expect(tv).toMatch(/className="dark [^"]*bg-background[^"]*text-foreground/);
  });

  test("the embed widget carries the new neutrals and maintenance blue", () => {
    const widget = read(status0, "src", "embed", "widget.ts");
    for (const old of ["#1f2937", "#e5e7eb", "#111827", "#374151", "#2563eb"]) {
      expect(widget).not.toContain(old);
    }
    for (const hex of ["#09121f", "#dee3eb", "#0c131e", "#edf2f9", "#1f293a", "#1e64ef"]) {
      expect(widget).toContain(hex);
    }
    // Status dots are unchanged.
    for (const hex of ["#16a34a", "#d97706", "#dc2626", "#9ca3af"]) {
      expect(widget).toContain(hex);
    }
  });
});
