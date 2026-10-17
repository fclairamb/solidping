import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { TrendingUp } from "lucide-react";

import { KpiTile } from "./kpi-tile";
import { badgeVariants } from "@/components/ui/badge";
import {
  AVAILABILITY_TIER_STYLE,
  type AvailabilityTier,
} from "@/lib/availability-tier";
import { contrastRatio, oklchToSrgb, parseOklch } from "@/lib/color-contrast";

// KpiTile has ONE flat look: every tile on a page is the same Card, with a
// sentence-case muted label, a bare muted icon and a semibold tabular value.
// Color goes on the value only, through valueClassName. The e2e suite
// (electric-identity-app-chrome.spec.ts) checks what the browser paints.

const css = readFileSync(join(__dirname, "../../index.css"), "utf8");

function block(selector: string): Record<string, string> {
  const start = css.indexOf(`\n${selector} {`);
  if (start < 0) throw new Error(`no ${selector} block in index.css`);
  const end = css.indexOf("\n}", start);
  const body = css.slice(start, end).replace(/\/\*[\s\S]*?\*\//g, "");
  const out: Record<string, string> = {};
  for (const m of body.matchAll(/--([\w-]+):\s*([^;]+);/g)) {
    out[m[1]] = m[2].replace(/\s+/g, " ").trim();
  }
  return out;
}

/** class list of the first tag carrying `marker` */
function classesAt(html: string, marker: string): string[] {
  const i = html.indexOf(marker);
  if (i < 0) throw new Error(`no ${marker} in the markup`);
  const tagStart = html.lastIndexOf("<", i);
  const tag = html.slice(tagStart, html.indexOf(">", i) + 1);
  const m = /class="([^"]*)"/.exec(tag);
  return m ? m[1].replace(/&amp;/g, "&").split(/\s+/) : [];
}

function render(extra: { valueClassName?: string } = {}) {
  return renderToStaticMarkup(
    <KpiTile
      label="24h Availability"
      value="99.87%"
      icon={<TrendingUp />}
      badge={<span data-testid="badge">Operational</span>}
      sub="Fleet uptime health"
      {...extra}
    />,
  );
}

describe("KpiTile", () => {
  const html = render();
  const root = classesAt(html, 'data-slot="kpi-tile"');

  it("is a plain Card whose hover deepens the shadow and never moves it", () => {
    expect(root).toEqual(
      expect.arrayContaining(["rounded-xl", "border", "bg-card", "shadow-card", "h-full"]),
    );
    expect(root).toContain("transition-shadow");
    expect(root).toContain("hover:shadow-card-hover");
    expect(root.filter((c) => c.includes("translate"))).toEqual([]);
    // Card's own dark-mode top-lit wash (dark:bg-gradient-to-b) is fine; no
    // electric gradient token, no tinted shadow.
    expect(root.filter((c) => /^bg-[a-z]+-gradient$/.test(c))).toEqual([]);
    expect(root.filter((c) => /shadow-(hero|tile|primary)/.test(c))).toEqual([]);
  });

  it("has no variant: there is no hero tile any more", () => {
    expect(html).not.toContain("data-variant");
    // A stale caller passing the removed prop renders the exact same tile.
    const stale = renderToStaticMarkup(
      <KpiTile
        {...({ variant: "hero" } as object)}
        label="24h Availability"
        value="99.87%"
        icon={<TrendingUp />}
        badge={<span data-testid="badge">Operational</span>}
        sub="Fleet uptime health"
      />,
    );
    expect(stale).toBe(html);
  });

  it("labels it in sentence case, muted", () => {
    const label = classesAt(html, 'data-slot="kpi-tile-label"');
    expect(label).toEqual(expect.arrayContaining(["text-sm", "font-medium", "text-muted-foreground"]));
    expect(label).not.toContain("uppercase");
    expect(label.filter((c) => c.startsWith("tracking-"))).toEqual([]);
  });

  it("shows the icon bare and muted, sized by the tile", () => {
    const icon = classesAt(html, 'data-slot="kpi-tile-icon"');
    expect(icon).toEqual(expect.arrayContaining(["text-muted-foreground", "[&_svg]:h-4", "[&_svg]:w-4"]));
    // No chip behind it.
    expect(icon.filter((c) => c.startsWith("bg-") || c.startsWith("rounded"))).toEqual([]);
  });

  it("sets the value semibold and tabular, larger from sm up", () => {
    const value = classesAt(html, 'data-slot="kpi-tile-value"');
    expect(value).toEqual(
      expect.arrayContaining(["text-2xl", "font-semibold", "tabular-nums", "sm:text-3xl", "text-foreground"]),
    );
    expect(value).not.toContain("font-bold");
  });

  it("valueClassName recolors the value through cn() and keeps its size", () => {
    const value = classesAt(
      render({ valueClassName: "text-status-error-foreground" }),
      'data-slot="kpi-tile-value"',
    );
    expect(value).toContain("text-status-error-foreground");
    expect(value).not.toContain("text-foreground");
    expect(value).toEqual(expect.arrayContaining(["text-2xl", "sm:text-3xl"]));
  });

  it("keeps the sub line small and muted, and the badge next to the value", () => {
    expect(classesAt(html, 'data-slot="kpi-tile-sub"')).toEqual(
      expect.arrayContaining(["text-xs", "text-muted-foreground"]),
    );
    expect(html.indexOf('data-testid="badge"')).toBeGreaterThan(html.indexOf('data-slot="kpi-tile-value"'));
    expect(html.indexOf('data-testid="badge"')).toBeLessThan(html.indexOf('data-slot="kpi-tile-sub"'));
  });
});

describe("availability tier style (dashboard availability tile)", () => {
  const EXPECTED: Record<AvailabilityTier, { badge: string; value: string }> = {
    noData: { badge: "secondary", value: "text-muted-foreground" },
    operational: { badge: "success", value: "text-foreground" },
    degraded: { badge: "warning", value: "text-status-warning-foreground" },
    down: { badge: "destructive", value: "text-status-error-foreground" },
  };

  it("maps every tier to a Badge variant and a value color, no data staying neutral", () => {
    expect(AVAILABILITY_TIER_STYLE).toEqual(EXPECTED);
  });

  it("names Badge variants that exist (cva drops an unknown one silently)", () => {
    const fallback = badgeVariants();
    for (const [tier, { badge }] of Object.entries(AVAILABILITY_TIER_STYLE)) {
      expect(badgeVariants({ variant: badge }), tier).not.toBe(fallback);
    }
  });

  const light = block(":root");
  const dark = block(".dark");
  it.each(
    Object.entries(AVAILABILITY_TIER_STYLE) as [AvailabilityTier, { value: string }][],
  )("%s: the value color is a mapped token, >= 4.5:1 on --card in both themes", (_tier, { value }) => {
    const name = /^text-([\w-]+)$/.exec(value)?.[1];
    expect(name, value).toBeTruthy();
    expect(css).toContain(`--color-${name}: var(--${name});`);
    for (const tokens of [light, dark]) {
      const ratio = contrastRatio(
        oklchToSrgb(parseOklch(tokens[name!])),
        oklchToSrgb(parseOklch(tokens.card)),
      );
      expect(ratio, `${name} on ${tokens === light ? "light" : "dark"} card`).toBeGreaterThanOrEqual(4.5);
    }
  });
});
