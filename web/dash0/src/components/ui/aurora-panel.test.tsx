import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { AuroraPanel } from "./aurora-panel";

// Spec 2026-09-24-03: the aurora panel re-tuned to the electric identity.
// These pin the class contract; e2e electric-identity-auth.spec.ts checks the
// painted colors on the real auth pages.

const layoutSource = readFileSync(
  join(__dirname, "..", "layout", "auth-split-layout.tsx"),
  "utf8",
);

const html = renderToStaticMarkup(
  <AuroraPanel className="p-4">
    <span>child</span>
  </AuroraPanel>,
);

function classesOf(markup: string): string[] {
  return [...markup.matchAll(/class="([^"]*)"/g)].flatMap((m) => m[1].split(/\s+/));
}

describe("AuroraPanel", () => {
  const classes = classesOf(html);

  it("sits on the sidebar navy, not slate", () => {
    const root = /<div[^>]*data-slot="aurora-panel"[^>]*class="([^"]*)"/.exec(html);
    expect(root).not.toBeNull();
    const rootClasses = root![1].split(/\s+/);
    expect(rootClasses).toEqual(
      expect.arrayContaining(["bg-sidebar", "bg-sidebar-gradient", "text-white", "p-4"]),
    );
    expect(classes).not.toContain("bg-slate-950");
  });

  it("glows cyan, primary and chart-5, with no crimson anywhere", () => {
    expect(classes).toEqual(
      expect.arrayContaining([
        "bg-aurora-cyan/40",
        "bg-primary/40",
        "bg-chart-5/30",
        "from-aurora-cyan/25",
        "to-chart-5/25",
      ]),
    );
    expect(classes.filter((c) => /brand/.test(c))).toEqual([]);
  });

  it("renders children above the glow on a z-10 layer", () => {
    expect(html).toMatch(/class="relative z-10[^"]*"><span>child<\/span>/);
  });
});

describe("AuthSplitLayout source", () => {
  it("has no hardcoded oklch: the accent comes from --aurora-accent", () => {
    expect(layoutSource).not.toMatch(/oklch/);
    expect(layoutSource).toContain("text-aurora-accent");
  });

  it("paints the page glow behind the form column", () => {
    expect(layoutSource).toMatch(/bg-page-glow/);
  });
});
