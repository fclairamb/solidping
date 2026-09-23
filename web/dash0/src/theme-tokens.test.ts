import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// Spec 2026-09-24-01 (electric identity): the token values live in index.css
// and the acceptance criteria are CONTRAST ratios, which a class-name test
// cannot see. This parses the real stylesheet, converts every oklch() the
// criteria name to sRGB the way a browser paints it on an sRGB screen
// (out-of-gamut channels clipped), and computes the WCAG ratio. Change a
// token so a label becomes unreadable and this goes red.

const css = readFileSync(join(__dirname, "index.css"), "utf8");

function block(selector: string): Record<string, string> {
  const start = css.indexOf(`${selector} {`);
  if (start < 0) throw new Error(`no ${selector} block in index.css`);
  const end = css.indexOf("\n}", start);
  const body = css.slice(start, end).replace(/\/\*[\s\S]*?\*\//g, "");
  const out: Record<string, string> = {};
  for (const m of body.matchAll(/--([\w-]+):\s*([^;]+);/g)) {
    out[m[1]] = m[2].replace(/\s+/g, " ").trim();
  }
  return out;
}

const light = block(":root");
const dark = block(".dark");

type Oklch = [number, number, number];

function parseOklch(value: string): Oklch {
  const m = /oklch\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)/.exec(value);
  if (!m) throw new Error(`not an oklch() color: ${value}`);
  return [Number(m[1]), Number(m[2]), Number(m[3])];
}

function gradientStops(value: string): Oklch[] {
  return [...value.matchAll(/oklch\([^)]*\)/g)].map((m) => parseOklch(m[0]));
}

function luminance([L, C, H]: Oklch): number {
  const h = (H * Math.PI) / 180;
  const a = C * Math.cos(h);
  const b = C * Math.sin(h);
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
  const linear = [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];
  // Clip to the sRGB gamut and quantize to 8 bits, as the painted pixel is.
  const [r, g, bl] = linear.map((v) => {
    const c = Math.min(1, Math.max(0, v));
    const encoded = c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055;
    const byte = Math.round(Math.min(1, Math.max(0, encoded)) * 255) / 255;
    return byte <= 0.04045 ? byte / 12.92 : ((byte + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * bl;
}

function contrast(a: Oklch, b: Oklch): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

const SPEC_LIGHT: Record<string, string> = {
  background: "oklch(0.98 0.006 250)",
  foreground: "oklch(0.18 0.03 258)",
  "card-foreground": "oklch(0.18 0.03 258)",
  "popover-foreground": "oklch(0.18 0.03 258)",
  card: "oklch(1 0 0)",
  popover: "oklch(1 0 0)",
  control: "oklch(1 0 0)",
  primary: "oklch(0.55 0.22 262)",
  "primary-foreground": "oklch(0.99 0 0)",
  secondary: "oklch(0.962 0.01 255)",
  muted: "oklch(0.962 0.01 255)",
  "secondary-foreground": "oklch(0.26 0.04 258)",
  "muted-foreground": "oklch(0.5 0.03 255)",
  accent: "oklch(0.95 0.03 255)",
  "accent-foreground": "oklch(0.42 0.18 262)",
  border: "oklch(0.915 0.012 255)",
  input: "oklch(0.87 0.015 255)",
  ring: "oklch(0.55 0.22 262)",
  "chart-1": "oklch(0.55 0.22 262)",
  "chart-5": "oklch(0.55 0.2 285)",
};

const SPEC_DARK: Record<string, string> = {
  background: "oklch(0.145 0.02 262)",
  foreground: "oklch(0.96 0.01 255)",
  "card-foreground": "oklch(0.96 0.01 255)",
  "popover-foreground": "oklch(0.96 0.01 255)",
  card: "oklch(0.185 0.026 262)",
  popover: "oklch(0.185 0.026 262)",
  control: "oklch(0.165 0.024 262)",
  primary: "oklch(0.72 0.15 252)",
  "primary-foreground": "oklch(0.16 0.03 262)",
  secondary: "oklch(0.225 0.03 262)",
  muted: "oklch(0.225 0.03 262)",
  "muted-foreground": "oklch(0.7 0.03 255)",
  accent: "oklch(0.3 0.07 260)",
  "accent-foreground": "oklch(0.92 0.05 255)",
  border: "oklch(0.28 0.035 262)",
  input: "oklch(0.33 0.04 262)",
  ring: "oklch(0.72 0.15 252)",
  "chart-1": "oklch(0.72 0.15 252)",
  "chart-5": "oklch(0.65 0.17 285)",
};

describe("electric identity tokens", () => {
  it.each([
    ["light", light, SPEC_LIGHT],
    ["dark", dark, SPEC_DARK],
  ] as const)("%s carries the spec's base token values", (_theme, actual, expected) => {
    for (const [name, value] of Object.entries(expected)) {
      expect(actual[name], `--${name}`).toBe(value);
    }
    expect(actual["chart-1"]).toBe(actual.primary);
  });

  it("defines the gradient tokens in both themes, identical except the glow", () => {
    for (const name of ["primary-gradient", "accent-gradient", "hero-gradient", "gradient-foreground"]) {
      expect(light[name], `light --${name}`).toBeTruthy();
      expect(dark[name], `dark --${name}`).toBe(light[name]);
    }
    expect(light["primary-gradient"]).toBe(
      "linear-gradient(135deg, oklch(0.56 0.17 242) 0%, oklch(0.53 0.22 262) 50%, oklch(0.48 0.23 276) 100%)",
    );
    expect(light["accent-gradient"]).toBe(
      "linear-gradient(135deg, oklch(0.7 0.15 225) 0%, oklch(0.57 0.22 258) 50%, oklch(0.5 0.23 275) 100%)",
    );
    expect(light["hero-gradient"]).toBe(
      "linear-gradient(135deg, oklch(0.62 0.17 232), oklch(0.5 0.23 265) 60%, oklch(0.38 0.19 280))",
    );
    expect(light["page-glow"]).toContain("oklch(0.7 0.15 225 / 0.13)");
    expect(light["page-glow"]).toContain("200px");
    expect(dark["page-glow"]).toContain("oklch(0.7 0.15 225 / 0.2)");
    expect(dark["page-glow"]).toContain("oklch(0.5 0.23 275 / 0.15)");
    expect(dark["page-glow"]).toContain("220px");
  });

  it("defines --chart-degraded as the status-warning color of each theme", () => {
    expect(light["chart-degraded"]).toBe(light["status-warning"]);
    expect(dark["chart-degraded"]).toBe(dark["status-warning"]);
  });

  it("exposes the gradient utilities and the Tailwind color mappings", () => {
    for (const name of ["primary", "accent", "hero"]) {
      expect(css).toMatch(
        new RegExp(`@utility bg-${name}-gradient \\{\\s*background-image: var\\(--${name}-gradient\\);`),
      );
    }
    expect(css).toContain("--color-gradient-foreground: var(--gradient-foreground);");
    expect(css).toContain("--color-chart-degraded: var(--chart-degraded);");
  });

  it("keeps the control-surface ordering (page < control <= card; dark recesses controls)", () => {
    const L = (v: string) => parseOklch(v)[0];
    expect(L(light.control)).toBeGreaterThan(L(light.background));
    expect(L(light.control)).toBeLessThanOrEqual(L(light.card));
    expect(L(dark.control)).toBeGreaterThan(L(dark.background));
    expect(L(dark.control)).toBeLessThan(L(dark.card));
  });
});

describe("electric identity contrast (acceptance criteria)", () => {
  const white = parseOklch(light["gradient-foreground"]);

  it("white text on --primary-gradient is >= 4.4:1 at every stop", () => {
    const stops = gradientStops(light["primary-gradient"]);
    expect(stops).toHaveLength(3);
    for (const stop of stops) {
      expect(contrast(white, stop), `stop ${stop.join(" ")}`).toBeGreaterThanOrEqual(4.4);
    }
  });

  it("positive control: the decorative --accent-gradient start is NOT safe for text", () => {
    // The reason there are two gradients. If this ever passes 4.4, the split
    // is no longer needed — or the math above is broken.
    const [start, middle] = gradientStops(light["accent-gradient"]);
    expect(contrast(white, start)).toBeLessThan(3);
    // The checkbox / stepper check glyph sits on the middle stop: 3:1 non-text rule.
    expect(contrast(white, middle)).toBeGreaterThanOrEqual(3);
  });

  it.each([
    ["light", light],
    ["dark", dark],
  ] as const)("%s: text-primary on --background is >= 4.5:1", (_theme, tokens) => {
    expect(contrast(parseOklch(tokens.primary), parseOklch(tokens.background))).toBeGreaterThanOrEqual(4.5);
    expect(contrast(parseOklch(tokens.primary), parseOklch(tokens.card))).toBeGreaterThanOrEqual(4.5);
  });

  it.each([
    ["light", light],
    ["dark", dark],
  ] as const)("%s: a solid bg-primary fill keeps a readable --primary-foreground", (_theme, tokens) => {
    expect(
      contrast(parseOklch(tokens["primary-foreground"]), parseOklch(tokens.primary)),
    ).toBeGreaterThanOrEqual(4.5);
  });
});
