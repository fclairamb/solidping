import type { Locator } from "@playwright/test";
import { test, expect, type Page } from "./fixtures";

// Spec 2026-09-24-01 (electric identity, tokens and primitives). Asserts what
// the browser actually PAINTS on the design reference page, because the two
// failure modes this spec has to avoid are invisible to a class-name test:
//   - tailwind-merge dropping `bg-primary` from under the gradient, or a flat
//     `bg-<color>` override leaving the gradient painted on top of it;
//   - a focus ring drawn flush against the blue gradient, where it vanishes.

/** Computed background-color of a throwaway element carrying `className`. */
async function probe(page: Page, className: string): Promise<string> {
  return page.evaluate((cls) => {
    const el = document.createElement("div");
    el.className = cls;
    document.body.appendChild(el);
    const bg = getComputedStyle(el).backgroundColor;
    el.remove();
    return bg;
  }, className);
}

async function paint(locator: Locator) {
  return locator.evaluate((el) => {
    const cs = getComputedStyle(el);
    return {
      image: cs.backgroundImage,
      color: cs.backgroundColor,
      text: cs.color,
      shadow: cs.boxShadow,
      translate: cs.translate,
    };
  });
}

async function openDesignReference(page: Page) {
  await page.goto("orgs/test/design-reference");
  await page.waitForLoadState("networkidle");
  await expect(page.getByTestId("gradient-button-group")).toBeVisible();
}

test.describe("electric identity primitives", () => {
  test("the default button paints the primary gradient over a bg-primary fill; its siblings stay flat", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await openDesignReference(page);
    const group = page.getByTestId("gradient-button-group");

    const primary = await paint(group.getByRole("button", { name: "Create check" }));
    expect(primary.image).toContain("linear-gradient");
    // The fallback fill survived tailwind-merge (the bug a stock cn() causes).
    expect(primary.color).toBe(await probe(page, "bg-primary"));
    // White label (--gradient-foreground) and the tinted shadow stack.
    expect(primary.text).toMatch(/^(oklch\(1 0 0\)|rgb\(255, 255, 255\))$/);
    expect(primary.shadow).not.toBe("none");
    expect(primary.shadow).toContain("inset");

    // One gradient per group: everything next to it is flat.
    for (const name of ["Cancel", "Details", "Remove"]) {
      const flat = await paint(group.getByRole("button", { name }));
      expect(flat.image, `${name} must not carry a gradient`).toBe("none");
    }
    // Positive control for the flat check: destructive is really red.
    const remove = await paint(group.getByRole("button", { name: "Remove" }));
    expect(remove.color).toBe(await probe(page, "bg-destructive"));
  });

  test("the focus ring is offset from the gradient so it stays visible", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await openDesignReference(page);
    const group = page.getByTestId("gradient-button-group");
    const create = group.getByRole("button", { name: "Create check" });

    const rest = await paint(create);
    // Keyboard navigation, so :focus-visible (not just :focus) applies.
    await group.getByRole("button", { name: "Cancel" }).focus();
    await page.keyboard.press("Shift+Tab");
    await expect(create).toBeFocused();
    expect(await create.evaluate((el) => el.matches(":focus-visible"))).toBe(true);

    await expect
      .poll(async () => (await paint(create)).shadow, { timeout: 3000 })
      .not.toBe(rest.shadow);
    const focused = await paint(create);
    // ring-offset-2 paints a 2px page-colored band, ring-2 a 2px ring around
    // it: the ring layer's spread is offset + width = 4px.
    expect(focused.shadow).toContain("0px 0px 0px 2px");
    expect(focused.shadow).toContain("0px 0px 0px 4px");
  });

  test("the hover lift only moves the button when motion is allowed", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await openDesignReference(page);
    const create = page
      .getByTestId("gradient-button-group")
      .getByRole("button", { name: "Create check" });

    await page.emulateMedia({ reducedMotion: "reduce" });
    await create.hover();
    await page.waitForTimeout(300);
    expect((await paint(create)).translate).toBe("none");

    await page.mouse.move(0, 0);
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await create.hover();
    await expect
      .poll(async () => (await paint(create)).translate, { timeout: 3000 })
      .toBe("0px -1px");
  });

  test("control on-states use the accent gradient and a full destructive progress bar is flat red", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await openDesignReference(page);
    const states = page.getByTestId("gradient-on-states");

    const switchOn = await paint(states.getByRole("switch", { checked: true }));
    const switchOff = await paint(states.getByRole("switch", { checked: false }));
    expect(switchOn.image).toContain("linear-gradient");
    expect(switchOff.image).toBe("none");

    const boxOn = await paint(states.getByRole("checkbox", { checked: true }));
    const boxOff = await paint(states.getByRole("checkbox", { checked: false }));
    expect(boxOn.image).toContain("linear-gradient");
    expect(boxOff.image).toBe("none");

    const partial = await paint(
      page.getByTestId("progress-partial").locator(":scope > div"),
    );
    expect(partial.image).toContain("linear-gradient");

    // The red fill must win: background-color red AND no gradient on top.
    const full = await paint(
      page.getByTestId("progress-full-destructive").locator(":scope > div"),
    );
    expect(full.image).toBe("none");
    expect(full.color).toBe(await probe(page, "bg-destructive"));

    // The bg-none escape hatch documented next to the utilities.
    const escaped = await paint(page.getByTestId("gradient-bg-none-example"));
    expect(escaped.image).toBe("none");
    expect(escaped.color).toBe(await probe(page, "bg-muted"));
  });
});
