import { test, expect } from "./fixtures";

/**
 * The destructive (delete) button is flat like every other variant: a solid
 * red with a hairline shadow-xs, and a hover that only changes the fill
 * (bg-destructive/90, wired in components/ui/button.tsx). The old red-tinted
 * elevation that grew on hover is gone.
 */
test.describe("Destructive button is flat", () => {
  test("keeps a hairline shadow at rest and on hover; hover changes the color only", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/design-reference");
    await page.waitForLoadState("networkidle");
    await page.emulateMedia({ reducedMotion: "no-preference" });

    // The "Button variants" row renders the live destructive variant.
    const destructive = page
      .getByRole("button", { name: "Destructive", exact: true })
      .first();
    await destructive.waitFor({ state: "visible" });
    await destructive.scrollIntoViewIfNeeded();

    const style = () =>
      destructive.evaluate((el) => {
        const cs = getComputedStyle(el);
        return {
          shadow: cs.boxShadow,
          color: cs.backgroundColor,
          image: cs.backgroundImage,
          translate: cs.translate,
          transform: cs.transform,
        };
      });
    const probe = (cls: string) =>
      page.evaluate((c) => {
        const el = document.createElement("div");
        el.className = c;
        document.body.appendChild(el);
        const bg = getComputedStyle(el).backgroundColor;
        el.remove();
        return bg;
      }, cls);

    // Tailwind always composes five box-shadow layers (inset, inset ring,
    // ring offset, ring, shadow); the unused ones are fully transparent.
    const visibleLayers = (shadow: string) =>
      shadow
        .split(/,(?![^(]*\))/)
        .map((layer) => layer.trim())
        .filter((layer) => !layer.startsWith("rgba(0, 0, 0, 0) "));

    const rest = await style();
    expect(rest.color).toBe(await probe("bg-destructive"));
    expect(rest.image).toBe("none");
    // shadow-xs: one neutral black 1px drop, no red tint and no second layer.
    expect(visibleLayers(rest.shadow)).toEqual(["rgba(0, 0, 0, 0.05) 0px 1px 2px 0px"]);
    expect(rest.shadow).not.toContain("inset");
    const restBox = (await destructive.boundingBox())!;

    await destructive.hover();
    const hoverColor = await probe("bg-destructive/90");
    expect(hoverColor).not.toBe(rest.color);
    await expect.poll(async () => (await style()).color, { timeout: 3000 }).toBe(hoverColor);

    const hovered = await style();
    expect(hovered.shadow).toBe(rest.shadow);
    expect(hovered.image).toBe("none");
    expect(hovered.translate).toBe("none");
    expect(hovered.transform).toBe("none");
    const box = (await destructive.boundingBox())!;
    expect(box.y).toBe(restBox.y);
  });
});
