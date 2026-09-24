import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { Globe } from "lucide-react";
import { PageHeader } from "./page-header";

// Spec 2026-09-24-02: the page-header icon tile is the brand accent gradient
// by default, and a flat neutral tile for third-party logos. The e2e suite
// (electric-identity-app-chrome.spec.ts) checks what the browser paints.

function classesOf(html: string, tagPattern: RegExp): string[] {
  const tag = tagPattern.exec(html)?.[0] ?? "";
  const m = /class="([^"]*)"/.exec(tag);
  return m ? m[1].split(/\s+/) : [];
}

const tile = (html: string) => classesOf(html, /<div[^>]*data-slot="page-header-tile"[^>]*>/);
const h1 = (html: string) => classesOf(html, /<h1[^>]*>/);

describe("PageHeader", () => {
  it("renders the accent-gradient brand tile by default", () => {
    const html = renderToStaticMarkup(<PageHeader icon={Globe} title="Checks" />);
    const c = tile(html);
    expect(html).toContain('data-tone="brand"');
    expect(c).toEqual(
      expect.arrayContaining([
        "bg-primary",
        "bg-accent-gradient",
        "text-gradient-foreground",
        "shadow-tile",
        "rounded-lg",
        "h-10",
        "w-10",
      ]),
    );
    expect(c).not.toContain("bg-muted");
    expect(c).not.toContain("rounded-md");
  });

  it("renders today's flat muted tile with tone=neutral", () => {
    const html = renderToStaticMarkup(
      <PageHeader icon={Globe} title="Slack" tone="neutral" />,
    );
    const c = tile(html);
    expect(html).toContain('data-tone="neutral"');
    expect(c).toEqual(expect.arrayContaining(["bg-muted", "text-foreground", "rounded-lg"]));
    expect(c.filter((cls) => cls.includes("gradient"))).toEqual([]);
    expect(c).not.toContain("shadow-tile");
  });

  it("titles the page with a bold, tightly tracked text-2xl h1", () => {
    const html = renderToStaticMarkup(<PageHeader icon={Globe} title="Checks" />);
    expect(h1(html)).toEqual(["text-2xl", "font-bold", "tracking-[-0.025em]"]);
  });

  it("a flat background override through iconClassName still drops the gradient (cn)", () => {
    const html = renderToStaticMarkup(
      <PageHeader icon={Globe} title="Checks" iconClassName="bg-transparent" />,
    );
    const c = tile(html);
    expect(c).toContain("bg-transparent");
    expect(c).not.toContain("bg-accent-gradient");
    expect(c).not.toContain("bg-primary");
  });
});
