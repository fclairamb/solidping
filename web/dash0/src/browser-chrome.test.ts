import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// Spec 2026-09-24-01 §6: the mobile browser bar continues the always-dark
// sidebar (navy #0a1731), not the old crimson #e91e63; the PWA splash
// background is the new page background.
const root = join(__dirname, "..");

describe("dash0 browser chrome color", () => {
  it("index.html sets theme-color to the sidebar navy", () => {
    const html = readFileSync(join(root, "index.html"), "utf8");
    const metas = [...html.matchAll(/<meta\s+name="theme-color"\s+content="([^"]+)"/g)].map(
      (m) => m[1],
    );
    expect(metas).toEqual(["#0a1731"]);
    expect(html).not.toContain("#e91e63");
  });

  it("the web manifest uses the navy theme and the page background", () => {
    const manifest = JSON.parse(
      readFileSync(join(root, "public", "manifest.webmanifest"), "utf8"),
    ) as { theme_color: string; background_color: string };
    expect(manifest.theme_color).toBe("#0a1731");
    expect(manifest.background_color).toBe("#f5f9fc");
  });
});
