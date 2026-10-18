import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// Spec 2026-09-24-06: the docs site carries the "electric" identity, in the
// form www.solidping.io wears it (solidping-website src/css/custom.css). These
// tests pin custom.css at the source level: no pink left, the Infima shades
// generated (not hand-picked) from the two bases, readable text and links on
// the page of each theme, and the brand tokens shared with the website.

const repo = join(import.meta.dir, "..", "..", "..", "..");
const css = readFileSync(join(import.meta.dir, "custom.css"), "utf8");
const emailHtml = readFileSync(
  join(repo, "server", "internal", "email", "templates", "base.html"),
  "utf8",
);
const footerCss = readFileSync(
  join(import.meta.dir, "..", "theme", "Footer", "Layout", "styles.module.css"),
  "utf8",
);

const code = css.replace(/\/\*[\s\S]*?\*\//g, "");

/** Custom properties declared in the first rule whose selector is `selector`. */
function declarations(selector: string): Map<string, string> {
  const at = code.indexOf(`${selector} {`);
  if (at < 0) throw new Error(`no "${selector}" rule`);
  const body = code.slice(code.indexOf("{", at) + 1, code.indexOf("}", at));
  const out = new Map<string, string>();
  for (const m of body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
    out.set(m[1], m[2].replace(/\s+/g, " ").trim());
  }
  return out;
}

const light = declarations(":root");
// html[...] like Infima's own dark block, which a bare [data-theme='dark']
// loses to on specificity.
const dark = declarations("html[data-theme='dark']");

// --- colour maths ---------------------------------------------------------

type RGB = [number, number, number];

const hexToRgb = (hex: string): RGB => {
  const h = hex.replace("#", "");
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16)) as RGB;
};

const rgbToHex = (rgb: number[]) =>
  "#" +
  rgb
    .map((v) =>
      Math.round(Math.max(0, Math.min(255, v)))
        .toString(16)
        .padStart(2, "0"),
    )
    .join("");

function rgbToHsl([r8, g8, b8]: RGB): RGB {
  const [r, g, b] = [r8 / 255, g8 / 255, b8 / 255];
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const l = (max + min) / 2;
  const d = max - min;
  let h = 0;
  let s = 0;
  if (d !== 0) {
    s = l <= 0.5 ? d / (max + min) : d / (2 - max - min);
    if (max === r) h = (g - b) / d;
    else if (max === g) h = 2 + (b - r) / d;
    else h = 4 + (r - g) / d;
    h = Math.min(h * 60, 360);
    if (h < 0) h += 360;
  }
  // color-convert (behind Docusaurus' generator) works in percent, and the
  // rounding at the end is sensitive to it.
  return [h, s * 100, l * 100];
}

function hslToRgb([h, sPct, lPct]: RGB): RGB {
  const s = sPct / 100;
  const l = lPct / 100;
  if (s === 0) return [l * 255, l * 255, l * 255];
  const t2 = l < 0.5 ? l * (1 + s) : l + s - l * s;
  const t1 = 2 * l - t2;
  const hue = h / 360;
  return [1, 0, -1].map((k) => {
    let t = hue + k / 3;
    if (t < 0) t++;
    if (t > 1) t--;
    let v = t1;
    if (6 * t < 1) v = t1 + (t2 - t1) * 6 * t;
    else if (2 * t < 1) v = t2;
    else if (3 * t < 2) v = t1 + (t2 - t1) * (2 / 3 - t) * 6;
    return v * 255;
  }) as RGB;
}

/** Docusaurus' palette generator: `Color(base).darken(x)` / `.lighten(x)`. */
function shade(base: string, ratio: number): string {
  const [h, s, l] = rgbToHsl(hexToRgb(base));
  return rgbToHex(hslToRgb([h, s, Math.max(0, Math.min(100, l + l * ratio))]));
}

const SHADES: Array<[string, number]> = [
  ["dark", -0.1],
  ["darker", -0.15],
  ["darkest", -0.3],
  ["light", 0.1],
  ["lighter", 0.15],
  ["lightest", 0.3],
];

function luminance(hex: string): number {
  const [r, g, b] = hexToRgb(hex).map((v) => {
    const c = v / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a: string, b: string): number {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p);
  return (x + 0.05) / (y + 0.05);
}

/** Follow `var(--x)` references through a theme's declarations. */
function resolve(decls: Map<string, string>, value: string): string {
  const m = value.match(/^var\((--[\w-]+)\)$/);
  if (!m) return value;
  const next = decls.get(m[1]) ?? light.get(m[1]);
  if (!next) throw new Error(`unresolved ${m[1]}`);
  return resolve(decls, next);
}

// --- tests ----------------------------------------------------------------

describe("docs custom.css electric identity", () => {
  test("no pink (#E91E63 family) is left", () => {
    const pink = [
      "#e91e63",
      "#d81b60",
      "#c2185b",
      "#ad1457",
      "#ec407a",
      "#f06292",
      "#f48fb1",
      "#f8bbd9",
      "#fce4ec",
      "#fff0f5",
    ];
    const lower = css.toLowerCase();
    for (const hex of pink) expect(lower).not.toContain(hex);
    expect(css).not.toMatch(/rgba\(\s*233\s*,\s*30\s*,\s*99/);
    expect(css).not.toMatch(/rgba\(\s*244\s*,\s*143\s*,\s*177/);
    expect(css.toLowerCase()).not.toContain("pink");
  });

  test("primaries match the electric --primary hex used by the emails", () => {
    expect(light.get("--ifm-color-primary")).toBe("#1e64ef");
    expect(dark.get("--ifm-color-primary")).toBe("#57a8ff");
    // base.html flattens the same dash0 --primary to hex.
    expect(emailHtml).toMatch(/#1E64EF/i);
    expect(emailHtml).toMatch(/#57A8FF/i);
  });

  test("the generator reproduces Docusaurus' own default shades", () => {
    // Positive control: Docusaurus' template green and its documented shades.
    expect(SHADES.map(([, r]) => shade("#2e8555", r))).toEqual([
      "#29784c",
      "#277148",
      "#205d3b",
      "#33925d",
      "#359962",
      "#3cad6e",
    ]);
  });

  for (const [theme, decls] of [
    ["light", light],
    ["dark", dark],
  ] as const) {
    test(`${theme} shades are generated from the base`, () => {
      const base = decls.get("--ifm-color-primary")!;
      for (const [name, ratio] of SHADES) {
        expect(decls.get(`--ifm-color-primary-${name}`)).toBe(shade(base, ratio));
      }
    });
  }

  test("text and links meet 4.5:1 against the page in both themes", () => {
    const failures: string[] = [];
    for (const [theme, decls] of [
      ["light", light],
      ["dark", dark],
    ] as const) {
      const merged = new Map([...light, ...decls]);
      const page = resolve(merged, merged.get("--ifm-background-color")!);
      for (const prop of [
        "--ifm-link-color",
        "--ifm-navbar-link-hover-color",
        // Active navbar and TOC links.
        "--ifm-color-primary",
        // Body copy, headings, the muted TOC and card text, the active
        // sidebar link.
        "--sp-text",
        "--sp-heading",
        "--sp-muted",
        "--sp-accent-text",
      ]) {
        const ratio = contrast(resolve(merged, merged.get(prop)!), page);
        if (ratio < 4.5) failures.push(`${theme} ${prop}: ${ratio.toFixed(2)}:1`);
      }
    }
    expect(failures).toEqual([]);
  });

  test("highlighted code line is the primary at 10% / 16%", () => {
    expect(light.get("--docusaurus-highlighted-code-line-bg")).toBe(
      "rgba(30, 100, 239, 0.1)",
    );
    expect(dark.get("--docusaurus-highlighted-code-line-bg")).toBe(
      "rgba(87, 168, 255, 0.16)",
    );
  });

  test("pages and brand tokens are the website's", () => {
    // White, not Infima's transparent light page, and the website's navy-black.
    expect(light.get("--ifm-background-color")).toBe("#ffffff");
    expect(dark.get("--ifm-background-color")).toBe("#0b1120");

    expect(light.get("--sp-navy-top")).toBe("#0a1731");
    expect(light.get("--sp-navy-bottom")).toBe("#04091b");
    expect(light.get("--sp-grad-button")).toBe(
      "linear-gradient(135deg, #007bce 0%, #175ee8 50%, #453cdb 100%)",
    );
    expect(light.get("--sp-grad-accent")).toBe(
      "linear-gradient(135deg, #00b0e4 0%, #175ee8 50%, #453cdb 100%)",
    );
    // Brand constants are declared once, for both themes.
    for (const token of ["--sp-navy", "--sp-grad-button", "--sp-grad-accent"]) {
      expect(dark.has(token)).toBe(false);
    }
  });

  test("dark tokens are not declared on a bare [data-theme='dark']", () => {
    // Infima's html[data-theme='dark'] would outrank it and keep its grey
    // #1b1b1d page. The html[...] block exists: declarations() found it.
    expect(dark.size).toBeGreaterThan(0);
    expect(code).not.toMatch(/(^|\n)\[data-theme='dark'\] \{/);
  });

  test("Start free is hidden unless the page is flagged as the cloud's", () => {
    // The flag is set by src/clientModules/deployment.ts; without this rule a
    // self-hosted copy of the docs would advertise the cloud.
    expect(code).toMatch(
      /html:not\(\[data-deployment='saas'\]\) \.navbar-cta \{\s*display: none;\s*\}/,
    );
  });

  test("buttons, the navbar CTA, the hero and the footer use the brand surfaces", () => {
    expect(code).toMatch(/\.button--primary \{[^}]*background: var\(--sp-grad-button\);/);
    expect(code).toMatch(/\.navbar-cta \{[^}]*background: var\(--sp-grad-button\);/);
    expect(code).toMatch(/\.sp-aurora \{[^}]*background: var\(--sp-navy\);/);
    expect(footerCss).toMatch(/\.footer \{[^}]*background: var\(--sp-navy\);/);
  });
});
