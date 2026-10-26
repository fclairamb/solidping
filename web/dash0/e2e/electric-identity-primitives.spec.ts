import type { Locator } from "@playwright/test";
import { test, expect, type Page } from "./fixtures";

// The primitives, as the browser PAINTS them on the design reference page:
// the one tonal --primary-gradient on the default button and the progress
// fill, everything else flat. A class-name test cannot see the failure modes
// that matter here:
//   - a utility that generates no CSS (an unmapped token) leaving the button
//     transparent, or tailwind-merge dropping the fill or the gradient;
//   - another gradient or a hover lift creeping back in through a variant;
//   - a focus ring drawn flush against the solid blue, where it vanishes.

/** Computed style of a throwaway element carrying `className`. */
async function probe(
  page: Page,
  className: string,
  prop: "backgroundColor" | "backgroundImage" | "color" | "borderTopColor" = "backgroundColor",
): Promise<string> {
  return page.evaluate(
    ([cls, p]) => {
      const el = document.createElement("div");
      el.className = cls;
      document.body.appendChild(el);
      const value = getComputedStyle(el)[p as "backgroundColor"];
      el.remove();
      return value;
    },
    [className, prop] as const,
  );
}

async function paint(locator: Locator) {
  return locator.evaluate((el) => {
    const cs = getComputedStyle(el);
    return {
      image: cs.backgroundImage,
      position: cs.backgroundPosition,
      size: cs.backgroundSize,
      color: cs.backgroundColor,
      text: cs.color,
      border: cs.borderTopColor,
      shadow: cs.boxShadow,
      translate: cs.translate,
      transform: cs.transform,
      radius: cs.borderTopLeftRadius,
    };
  });
}

const WHITE = /^(oklch\(1 0 0\)|rgb\(255, 255, 255\))$/;

// Tailwind always composes five box-shadow layers (inset, inset ring, ring
// offset, ring, shadow); the unused ones are fully transparent.
function visibleLayers(shadow: string): string[] {
  return shadow
    .split(/,(?![^(]*\))/)
    .map((layer) => layer.trim())
    .filter((layer) => !layer.startsWith("rgba(0, 0, 0, 0) "));
}

const SHADOW_XS = "rgba(0, 0, 0, 0.05) 0px 1px 2px 0px";

async function applyTheme(page: Page, theme: "light" | "dark") {
  await page.evaluate((t) => localStorage.setItem("theme", t), theme);
  await page.reload();
  await expect
    .poll(() => page.evaluate(() => document.documentElement.classList.contains("dark")))
    .toBe(theme === "dark");
  await expect(page.getByTestId("primary-button-group")).toBeVisible();
}

async function openDesignReference(page: Page, theme: "light" | "dark" = "light") {
  await page.goto("orgs/test/design-reference");
  await expect(page.getByTestId("primary-button-group")).toBeVisible();
  await applyTheme(page, theme);
}

/** The painted --primary-gradient, as a bg-primary-gradient element shows it. */
const gradientImage = (page: Page) => probe(page, "bg-primary-gradient", "backgroundImage");

test.describe("tonal primitives", () => {
  for (const theme of ["light", "dark"] as const) {
    test(`the default button paints the primary gradient over the fill with a white label; its siblings stay flat (${theme})`, async ({
      authenticatedPage,
    }) => {
      const page = authenticatedPage;
      await openDesignReference(page, theme);
      const group = page.getByTestId("primary-button-group");

      const primary = await paint(group.getByRole("button", { name: "Create check" }));
      const gradient = await gradientImage(page);
      expect(gradient).toContain("linear-gradient");
      expect(primary.image).toBe(gradient);
      // 200%: at rest the button shows the lighter half.
      expect(primary.size).toBe("200% 200%");
      expect(primary.position).toBe("0% 0%");
      // The flat fill stays underneath (tailwind-merge kept both).
      expect(primary.color).toBe(await probe(page, "bg-primary-fill"));
      // Positive control: the probe tells the fill from the text blue.
      expect(primary.color).not.toBe(await probe(page, "bg-primary"));
      expect(primary.text).toMatch(WHITE);
      // A hairline neutral drop: no inset highlight, no tinted elevation.
      expect(visibleLayers(primary.shadow)).toEqual([SHADOW_XS]);
      expect(primary.shadow).not.toContain("inset");
      expect(primary.radius).toBe("8px");

      for (const name of ["Cancel", "Details", "Remove"]) {
        const flat = await paint(group.getByRole("button", { name }));
        expect(flat.image, `${name} must not carry a gradient`).toBe("none");
        expect(flat.radius, name).toBe("8px");
      }
      // Positive control for the flat check: destructive is really red.
      const remove = await paint(group.getByRole("button", { name: "Remove" }));
      expect(remove.color).toBe(
        await probe(page, theme === "dark" ? "bg-destructive/80" : "bg-destructive"),
      );
      expect(remove.text).toMatch(WHITE);

      await applyTheme(page, "light");
    });
  }

  test("the focus ring is offset from the fill so it stays visible", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await openDesignReference(page);
    const group = page.getByTestId("primary-button-group");
    const create = group.getByRole("button", { name: "Create check" });

    const rest = await paint(create);
    // Keyboard navigation, so :focus-visible (not just :focus) applies.
    await group.getByRole("button", { name: "Cancel" }).focus();
    await page.keyboard.press("Shift+Tab");
    await expect(create).toBeFocused();
    expect(await create.evaluate((el) => el.matches(":focus-visible"))).toBe(true);

    // ring-offset-2 paints a 2px page-colored band, ring-2 a 2px ring around
    // it: the ring layer's spread is offset + width = 4px.
    await expect
      .poll(async () => (await paint(create)).shadow, { timeout: 3000 })
      .toContain("0px 0px 0px 4px");
    const focused = await paint(create);
    expect(focused.shadow).not.toBe(rest.shadow);
    expect(focused.shadow).toContain("0px 0px 0px 2px");
    expect(focused.shadow).not.toContain("inset");
  });

  test("hover changes the color (or slides the gradient), never the position", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await openDesignReference(page);
    // Motion allowed: the old lift was gated on motion-safe, so this is the
    // case where it would show.
    await page.emulateMedia({ reducedMotion: "no-preference" });
    const group = page.getByTestId("primary-button-group");
    const gradient = await gradientImage(page);

    const hoverAndCheck = async (name: string) => {
      const button = group.getByRole("button", { name });
      // Scrolled into place first, so hover() itself moves nothing.
      await button.scrollIntoViewIfNeeded();
      await page.mouse.move(0, 0);
      const rest = await paint(button);
      const restBox = (await button.boundingBox())!;
      await button.hover();
      return { button, rest, restBox };
    };
    const expectStill = async (
      name: string,
      button: Locator,
      rest: Awaited<ReturnType<typeof paint>>,
      restBox: { x: number; y: number },
    ) => {
      const hovered = await paint(button);
      expect(hovered.translate, name).toBe("none");
      expect(hovered.transform, name).toBe("none");
      expect(hovered.shadow, `${name}: the shadow does not grow`).toBe(rest.shadow);
      expect(hovered.text, name).toMatch(WHITE);
      const box = (await button.boundingBox())!;
      expect(box.y, name).toBe(restBox.y);
      expect(box.x, name).toBe(restBox.x);
      return hovered;
    };

    // The default button slides its gradient to the darker half.
    {
      const { button, rest, restBox } = await hoverAndCheck("Create check");
      expect(rest.position).toBe("0% 0%");
      await expect
        .poll(async () => (await paint(button)).position, { timeout: 3000 })
        .toBe("100% 100%");
      const hovered = await expectStill("Create check", button, rest, restBox);
      expect(hovered.image).toBe(gradient);
    }

    // A flat variant changes its color.
    {
      const { button, rest, restBox } = await hoverAndCheck("Remove");
      const hoverColor = await probe(page, "bg-destructive/90");
      expect(hoverColor, "Remove: the hover color differs from rest").not.toBe(rest.color);
      await expect.poll(async () => (await paint(button)).color, { timeout: 3000 }).toBe(hoverColor);
      const hovered = await expectStill("Remove", button, rest, restBox);
      expect(hovered.image).toBe("none");
    }
  });

  test("control on-states paint the solid primary fill, the progress fill adds the gradient, a full destructive bar is red", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await openDesignReference(page);
    const states = page.getByTestId("control-on-states");
    const fill = await probe(page, "bg-primary-fill");

    const switchOn = await paint(states.getByRole("switch", { checked: true }));
    const switchOff = await paint(states.getByRole("switch", { checked: false }));
    expect(switchOn.image).toBe("none");
    expect(switchOn.color).toBe(fill);
    expect(switchOff.image).toBe("none");
    expect(switchOff.color).toBe(await probe(page, "bg-input"));

    const boxOn = await paint(states.getByRole("checkbox", { checked: true }));
    const boxOff = await paint(states.getByRole("checkbox", { checked: false }));
    expect(boxOn.image).toBe("none");
    expect(boxOn.color).toBe(fill);
    // The border takes the fill too, so the box reads as one shape.
    expect(boxOn.border).toBe(await probe(page, "border border-primary-fill", "borderTopColor"));
    expect(boxOn.text).toMatch(WHITE);
    expect(boxOff.image).toBe("none");
    expect(boxOff.color).not.toBe(fill);

    const partial = await paint(page.getByTestId("progress-partial").locator(":scope > div"));
    expect(partial.image).toBe(await gradientImage(page));
    expect(partial.color).toBe(fill);

    const full = await paint(
      page.getByTestId("progress-full-destructive").locator(":scope > div"),
    );
    expect(full.image).toBe("none");
    expect(full.color).toBe(await probe(page, "bg-destructive"));
  });

  test("the buttons and controls section paints no gradient but the primary one, and only where it belongs", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await openDesignReference(page);
    const gradient = await gradientImage(page);
    const scan = () =>
      page.evaluate(() => {
        const section = document.querySelector("section#buttons-badges");
        if (!section) throw new Error("no #buttons-badges section");
        return [section, ...section.querySelectorAll("*")]
          .filter((el) => getComputedStyle(el).backgroundImage.includes("gradient"))
          .map((el) => ({
            image: getComputedStyle(el).backgroundImage,
            tag: el.tagName.toLowerCase(),
            role: el.getAttribute("role"),
            html: el.outerHTML.slice(0, 120),
          }));
      });
    const painted = await scan();
    // Positive control: the default buttons and the progress fill are there.
    expect(painted.filter((p) => p.tag === "button").length).toBeGreaterThan(0);
    for (const p of painted) {
      expect(p.image, p.html).toBe(gradient);
      // Never on a switch or a checkbox.
      expect(p.role ?? "", p.html).not.toMatch(/^(switch|checkbox)$/);
    }
    for (const role of ["switch", "checkbox"] as const) {
      for (const el of await page.locator("section#buttons-badges").getByRole(role).all()) {
        expect((await paint(el)).image, role).toBe("none");
      }
    }

    // Positive control: the same scan sees a foreign gradient when one is there.
    await page.evaluate(() => {
      const el = document.createElement("div");
      el.className = "bg-page-glow";
      el.dataset.probe = "glow";
      document.querySelector("section#buttons-badges")!.appendChild(el);
    });
    const foreign = (await scan()).filter((p) => p.image !== gradient);
    expect(foreign).toHaveLength(1);
    expect(foreign[0].image).toContain("radial-gradient");
  });

  test("the design reference documents the gradient rules, the primary fill and the auth-only glow", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await openDesignReference(page);

    for (const id of [
      "gradient-rules",
      "primary-fill-rule",
      "primary-fill-example",
      "primary-gradient-example",
      "brand-color-rule",
      "primary-gradient-swatch",
      "page-glow-swatch",
    ]) {
      await expect(page.getByTestId(id), id).toBeVisible();
    }

    // The solid example paints the token with its white label.
    const example = page.getByTestId("primary-fill-solid-example");
    const solid = await paint(example);
    expect(solid.color).toBe(await probe(page, "bg-primary-fill"));
    expect(solid.image).toBe("none");
    expect(solid.text).toMatch(WHITE);
    // The gradient example paints it over the same fill.
    const gradient = await gradientImage(page);
    const graded = await paint(page.getByTestId("primary-gradient-example"));
    expect(graded.image).toBe(gradient);
    expect(graded.color).toBe(await probe(page, "bg-primary-fill"));
    expect(graded.text).toMatch(WHITE);

    const tokens = page.locator("section#color-tokens");
    const swatch = (varName: string) =>
      tokens.locator(`[data-testid="color-swatch"][data-var-name="${varName}"]`);
    const swatchBox = (varName: string) => swatch(varName).getByTestId("color-swatch-box");
    // The retired gradient tokens have no swatch any more.
    for (const token of ["--accent-gradient", "--hero-gradient"]) {
      await expect(swatch(token), token).toHaveCount(0);
    }
    // The primary gradient swatch paints the token; the glow swatch survives
    // (auth screens only) and is a real gradient.
    expect((await paint(page.getByTestId("primary-gradient-swatch"))).image).toBe(gradient);
    expect((await paint(page.getByTestId("page-glow-swatch"))).image).toContain("radial-gradient");

    // --primary-fill and --chart-degraded swatches paint their token, which
    // swaps with the theme like every other.
    const fillSwatch = swatchBox("--primary-fill");
    const degraded = swatchBox("--chart-degraded");
    const lightFill = (await paint(fillSwatch)).color;
    const lightDegraded = (await paint(degraded)).color;
    expect(lightFill).toBe(await probe(page, "bg-primary-fill"));
    expect(lightDegraded).toBe(await probe(page, "bg-status-warning"));

    const isDark = await page.evaluate(() => document.documentElement.classList.contains("dark"));
    await page.getByTestId("theme-toggle").click();
    await expect
      .poll(() => page.evaluate(() => document.documentElement.classList.contains("dark")))
      .toBe(!isDark);
    await expect.poll(async () => (await paint(degraded)).color).not.toBe(lightDegraded);
    await expect.poll(async () => (await paint(fillSwatch)).color).not.toBe(lightFill);
    expect((await paint(degraded)).color).toBe(await probe(page, "bg-status-warning"));
    expect((await paint(fillSwatch)).color).toBe(await probe(page, "bg-primary-fill"));
    await expect(page.getByTestId("gradient-rules")).toBeVisible();
  });
});
