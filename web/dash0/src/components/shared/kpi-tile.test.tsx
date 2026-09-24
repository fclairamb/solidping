import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { TrendingUp } from "lucide-react";

import { KpiTile } from "./kpi-tile";
import {
  AVAILABILITY_TIER_HERO_BADGE,
  type AvailabilityTier,
} from "@/lib/availability-tier";
import {
  composite,
  contrastRatio,
  gradientColorAt,
  gradientStops,
  oklchToSrgb,
  parseOklch,
} from "@/lib/color-contrast";

// Spec 2026-09-24-02: KpiTile lives in components/shared with a "hero"
// variant. The hero puts SMALL white text on --hero-gradient, whose light cyan
// start only gives white ~3.4:1. These tests compute the contrast the browser
// actually paints, from the tile's real classes and the real token.

const css = readFileSync(join(__dirname, "../../index.css"), "utf8");
const tailwindTheme = readFileSync(
  join(__dirname, "../../../node_modules/tailwindcss/theme.css"),
  "utf8",
);

function token(name: string): string {
  const m = new RegExp(`\\n:root \\{[\\s\\S]*?--${name}:\\s*([^;]+);`).exec(css);
  if (!m) throw new Error(`no --${name} in :root`);
  return m[1].replace(/\s+/g, " ").trim();
}

function tailwindColor(name: string): string {
  const m = new RegExp(`--color-${name}:\\s*([^;]+);`).exec(tailwindTheme);
  if (!m) throw new Error(`no --color-${name} in tailwind's theme`);
  return m[1];
}

/** class list of the first tag carrying `marker` */
function classesAt(html: string, marker: string): string[] {
  const i = html.indexOf(marker);
  if (i < 0) throw new Error(`no ${marker} in the markup`);
  const tagStart = html.lastIndexOf("<", i);
  const tag = html.slice(tagStart, html.indexOf(">", i) + 1);
  const m = /class="([^"]*)"/.exec(tag);
  return m ? m[1].split(/\s+/) : [];
}

function render(variant: "default" | "hero") {
  return renderToStaticMarkup(
    <KpiTile
      variant={variant}
      label="24h Availability"
      value="99.87%"
      icon={<TrendingUp className="h-4 w-4" />}
      badge={<span data-testid="badge">Operational</span>}
      sub="Fleet uptime health"
    />,
  );
}

describe("KpiTile, default variant", () => {
  const html = render("default");
  const root = classesAt(html, 'data-slot="kpi-tile"');

  it("is a Card that lifts on hover, only under motion-safe", () => {
    expect(html).toContain('data-variant="default"');
    expect(root).toEqual(expect.arrayContaining(["rounded-xl", "border", "bg-card", "shadow-card"]));
    expect(root).toContain("motion-safe:hover:-translate-y-0.5");
    expect(root).toContain("hover:shadow-card-hover");
    expect(root.filter((c) => c.includes("translate") && !c.startsWith("motion-safe:"))).toEqual([]);
    // Card's own dark-mode top-lit wash (dark:bg-gradient-to-b) is fine; no
    // electric gradient token on a default tile.
    expect(root.filter((c) => /^bg-[a-z]+-gradient$/.test(c))).toEqual([]);
  });

  it("keeps the muted label, sub and icon chip", () => {
    expect(classesAt(html, 'data-slot="kpi-tile-label"')).toContain("text-muted-foreground");
    expect(classesAt(html, 'data-slot="kpi-tile-sub"')).toContain("text-muted-foreground");
    expect(classesAt(html, 'data-slot="kpi-tile-icon"')).toContain("bg-muted/60");
  });
});

describe("KpiTile, hero variant", () => {
  const html = render("hero");
  const root = classesAt(html, 'data-slot="kpi-tile"');
  const label = classesAt(html, 'data-slot="kpi-tile-label"');
  const sub = classesAt(html, 'data-slot="kpi-tile-sub"');
  const value = classesAt(html, 'data-slot="kpi-tile-value"');

  it("paints the hero gradient with no border and white text", () => {
    expect(html).toContain('data-variant="hero"');
    expect(root).toEqual(
      expect.arrayContaining(["bg-hero-gradient", "bg-primary", "text-gradient-foreground", "rounded-xl"]),
    );
    expect(root).not.toContain("border");
    expect(root).not.toContain("bg-card");
    expect(value).toContain("text-gradient-foreground");
    expect(classesAt(html, 'data-slot="kpi-tile-icon"')).toEqual(
      expect.arrayContaining(["bg-white/15", "text-gradient-foreground"]),
    );
  });

  it("uses the tinted hero shadow at rest and on hover, never shadow-card-hover", () => {
    expect(root).toContain("shadow-hero");
    expect(root).toContain("hover:shadow-hero");
    expect(root).not.toContain("hover:shadow-card-hover");
    expect(root).not.toContain("shadow-card");
    expect(root).toContain("motion-safe:hover:-translate-y-0.5");
    expect(root.filter((c) => c.includes("translate") && !c.startsWith("motion-safe:"))).toEqual([]);
    expect(css).toContain(
      "--shadow-hero: 0 14px 28px -12px color-mix(in oklab, var(--primary) 60%, transparent);",
    );
  });

  // The rendered-contrast proof. The tile crops the gradient with
  // bg-size-[N%_N%] anchored bg-bottom-right: a 135deg gradient drawn on a
  // box N% of the tile's size, of which the tile shows the bottom-right
  // corner, covers t in [1 - 100/N, 1] whatever the tile's aspect ratio. The
  // small text is white at alpha A. Both numbers come from the classes.
  const stops = gradientStops(token("hero-gradient"));
  const white = oklchToSrgb(parseOklch(token("gradient-foreground")));
  const size = root.map((c) => /^bg-size-\[(\d+)%_(\d+)%\]$/.exec(c)).find(Boolean);
  const scale = size ? Number(size[1]) / 100 : 1;
  const firstVisible = 1 - 1 / scale;
  const alphaOf = (classes: string[]) => {
    const m = classes.map((c) => /^text-gradient-foreground\/(\d+)$/.exec(c)).find(Boolean);
    return m ? Number(m[1]) / 100 : 1;
  };
  const minContrast = (alpha: number, from: number) => {
    let min = Infinity;
    for (let t = from; t <= 1.000001; t += 0.005) {
      const bg = gradientColorAt(stops, t);
      min = Math.min(min, contrastRatio(composite(white, alpha, bg), bg));
    }
    return min;
  };

  it("crops the gradient to its darker end, anchored bottom-right, as a 135deg token", () => {
    expect(token("hero-gradient")).toMatch(/^linear-gradient\(135deg,/);
    expect(size?.[1]).toBe(size?.[2]);
    expect(root).toContain("bg-bottom-right");
    expect(scale).toBeGreaterThan(1);
  });

  it("keeps every small line >= 4.5:1 over every visible point of the gradient", () => {
    for (const [name, classes] of [
      ["label", label],
      ["sub", sub],
    ] as const) {
      const alpha = alphaOf(classes);
      expect(alpha, `${name} is translucent white`).toBeLessThan(1);
      expect(alpha, `${name} alpha`).toBeGreaterThanOrEqual(0.8);
      expect(minContrast(alpha, firstVisible), `${name} at alpha ${alpha}`).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("positive controls: neither the crop nor the 90% alone would be enough", () => {
    const alpha = alphaOf(label);
    // Uncropped, the light cyan start fails even for pure white small text.
    expect(minContrast(1, 0)).toBeLessThan(4.5);
    expect(minContrast(alpha, 0)).toBeLessThan(4.5);
    // Cropped, the spec's literal 80% would still fail at the lightest point.
    expect(minContrast(0.8, firstVisible)).toBeLessThan(4.5);
  });

  it("the value is large text (>= 24px bold) and clears 3:1 everywhere", () => {
    expect(value).toEqual(expect.arrayContaining(["text-2xl", "font-bold"]));
    expect(minContrast(1, firstVisible)).toBeGreaterThanOrEqual(3);
  });
});

describe("hero tier badge (dashboard availability tile)", () => {
  it.each(Object.entries(AVAILABILITY_TIER_HERO_BADGE) as [AvailabilityTier, string][])(
    "%s is a solid white chip with >= 4.5:1 text, the same in both themes",
    (_tier, classes) => {
      const list = classes.split(/\s+/);
      expect(list).toContain("bg-white");
      expect(list.filter((c) => c.startsWith("dark:"))).toEqual([]);
      const text = list.map((c) => /^text-([a-z]+-\d+)$/.exec(c)).find(Boolean);
      expect(text, `${classes} has a palette text color`).toBeTruthy();
      const color = oklchToSrgb(parseOklch(tailwindColor(text![1])));
      expect(contrastRatio(color, [1, 1, 1])).toBeGreaterThanOrEqual(4.5);
    },
  );

  it("documents that --destructive and this badge's red-700 are separate, uncoupled colors", () => {
    // "down" uses a plain Tailwind color, matching the other three tiers'
    // own fixed palette, rather than text-destructive. Before spec
    // 2026-09-24-07 darkened --destructive for the destructive button/text,
    // that token was actually too light for this badge (4.41:1 on white);
    // it now also clears 4.5:1, but the two stayed uncoupled regardless —
    // this only pins that they are, in fact, different colors.
    expect(token("destructive")).not.toBe(tailwindColor("red-700"));
    expect(
      contrastRatio(oklchToSrgb(parseOklch(token("destructive"))), [1, 1, 1]),
    ).toBeGreaterThanOrEqual(4.5);
  });
});
