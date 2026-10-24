import { test, expect, type Page } from "./fixtures";

// App chrome as the browser actually paints it. The unit tests pin the class
// contract. Spec 2026-09-24-02 made the sidebar always-dark navy (desktop,
// icon rail, mobile sheet) with a cyan active marker, and that is unchanged.
// The flat redesign removed the glows and the blue-to-violet gradients; the
// tonal pass brought back one gradient, --primary-gradient (page-header tile,
// default button, progress fill), and status washes that fade to transparent
// (KPI tile tones, the status banner). The page glow stays out of the org app.

const NAVY_LIGHT = "oklch(0.18 0.05 263)";
const NAVY_DARK = "oklch(0.125 0.035 263)";
const SIDEBAR_BORDER_LIGHT = "oklch(0.27 0.05 262)";
const SIDEBAR_BORDER_DARK = "oklch(0.22 0.035 262)";
const SIDEBAR_MUTED_FG = "oklch(0.66 0.04 255)";
const SIDEBAR_CYAN = "oklch(0.75 0.14 220)";
const PAGE_BG_LIGHT = "oklch(0.98 0.006 250)";
// The dark --muted-foreground: what a plain text-muted-foreground resolves to
// INSIDE the dark-scoped sidebar, in light mode too.
const DARK_MUTED_FG = "oklch(0.7 0.03 255)";

async function useTheme(page: Page, theme: "light" | "dark") {
  await page.evaluate((t) => localStorage.setItem("theme", t), theme);
  await page.reload();
  await page.waitForLoadState("networkidle");
  await expect
    .poll(() => page.evaluate(() => document.documentElement.classList.contains("dark")))
    .toBe(theme === "dark");
}

/** Computed style of the desktop sidebar pieces, read in one round trip. */
async function sidebarPaint(page: Page) {
  return page.evaluate(() => {
    // A custom property reads back as its raw (minified in a production
    // build) text; resolve it through a probe's computed color instead.
    const resolve = (host: Element, name: string) => {
      const probe = document.createElement("span");
      probe.style.color = `var(${name})`;
      host.appendChild(probe);
      const color = getComputedStyle(probe).color;
      probe.remove();
      return color;
    };
    const root = document.querySelector<HTMLElement>('[data-slot="sidebar"].group');
    const inner = document.querySelector<HTMLElement>('[data-slot="sidebar-inner"]');
    const container = document.querySelector<HTMLElement>('[data-slot="sidebar-container"]');
    const label = document.querySelector<HTMLElement>('[data-sidebar="group-label"]');
    const version = document.querySelector<HTMLElement>('[data-testid="server-version-text"]');
    if (!root || !inner || !container || !label) throw new Error("sidebar not rendered");
    return {
      rootIsDark: root.classList.contains("dark"),
      sidebarVar: resolve(root, "--sidebar"),
      innerSidebarVar: resolve(inner, "--sidebar"),
      background: getComputedStyle(inner).backgroundColor,
      image: getComputedStyle(inner).backgroundImage,
      border: getComputedStyle(container).borderRightColor,
      borderWidth: getComputedStyle(container).borderRightWidth,
      label: getComputedStyle(label).color,
      version: version ? getComputedStyle(version).color : null,
      body: getComputedStyle(document.body).backgroundColor,
    };
  });
}

test.describe("always-dark sidebar", () => {
  test("is navy in light mode, with the light-mode token values on the element itself", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await useTheme(page, "light");

    const paint = await sidebarPaint(page);
    // The page is light …
    expect(paint.body).toBe(PAGE_BG_LIGHT);
    // … the sidebar carries the dark scope …
    expect(paint.rootIsDark).toBe(true);
    // … and yet its --sidebar is the LIGHT-mode navy: the class did not
    // re-pin the token to its dark-mode value (spec §1, "Add a check").
    expect(paint.sidebarVar).toBe(NAVY_LIGHT);
    expect(paint.innerSidebarVar).toBe(NAVY_LIGHT);
    expect(paint.background).toBe(NAVY_LIGHT);
    expect(paint.image).toContain("linear-gradient");
    expect(paint.border).toBe(SIDEBAR_BORDER_LIGHT);
    expect(paint.borderWidth).toBe("1px");
    expect(paint.label).toBe(SIDEBAR_MUTED_FG);
    // A plain text-muted-foreground nested in the sidebar reads the DARK token.
    if (paint.version !== null) expect(paint.version).toBe(DARK_MUTED_FG);

    // The theme toggle reads the DOCUMENT theme, not the sidebar's class: in
    // light mode it still offers "dark mode" and shows the moon.
    const toggle = page.getByTestId("theme-toggle");
    await expect(toggle).toHaveAttribute("aria-label", /dark mode/i);
    await expect(toggle.locator("svg.lucide-moon")).toHaveCount(1);
  });

  test("is a little deeper in dark mode, and the toggle flips the page, not the sidebar", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await useTheme(page, "light");

    await page.getByTestId("theme-toggle").click();
    await expect
      .poll(() => page.evaluate(() => document.documentElement.classList.contains("dark")))
      .toBe(true);
    await expect(page.getByTestId("theme-toggle")).toHaveAttribute("aria-label", /light mode/i);

    const paint = await sidebarPaint(page);
    expect(paint.rootIsDark).toBe(true);
    expect(paint.sidebarVar).toBe(NAVY_DARK);
    expect(paint.background).toBe(NAVY_DARK);
    expect(paint.image).toContain("linear-gradient");
    expect(paint.border).toBe(SIDEBAR_BORDER_DARK);
    expect(paint.label).toBe(SIDEBAR_MUTED_FG);

    await useTheme(page, "light");
  });

  test("the active item shows the wash and a cyan marker flush on the sidebar edge", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await useTheme(page, "light");

    const active = page
      .getByTestId("app-sidebar")
      .locator('[data-sidebar="menu-button"][data-active="true"]');
    await expect(active).toHaveCount(1);
    await expect(active).toContainText("Dashboard");
    await expect(active).toHaveCSS("font-weight", "600");
    await expect(active).toHaveCSS("background-image", /linear-gradient/);

    const marker = await active.evaluate((el) => {
      const before = getComputedStyle(el, "::before");
      const item = el.closest('[data-sidebar="menu-item"]')!.getBoundingClientRect();
      const inner = document.querySelector('[data-slot="sidebar-inner"]')!.getBoundingClientRect();
      return {
        position: before.position,
        width: before.width,
        color: before.backgroundColor,
        radius: before.borderRadius,
        // `left` resolves against the menu item (the containing block).
        barLeft: item.left + parseFloat(before.left),
        innerLeft: inner.left,
      };
    });
    expect(marker.position).toBe("absolute");
    expect(marker.width).toBe("3px");
    expect(marker.color).toBe(SIDEBAR_CYAN);
    expect(marker.radius).toBe("0px 3px 3px 0px");
    expect(Math.abs(marker.barLeft - marker.innerLeft)).toBeLessThanOrEqual(1);

    // An inactive item has no marker and no wash.
    const checks = page
      .getByTestId("app-sidebar")
      .getByRole("link", { name: "Checks", exact: true });
    await expect(checks).toHaveAttribute("data-active", "false");
    await expect(checks).toHaveCSS("background-image", "none");
    expect(
      await checks.evaluate((el) => getComputedStyle(el, "::before").content),
    ).toBe("none");
  });

  test("the collapsed icon rail keeps the marker inside the 48px rail", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await useTheme(page, "light");

    const sidebar = page.getByTestId("app-sidebar");
    // Same technique as sidebar.spec.ts: AppSidebar is offcanvas, so drive
    // the exact data attributes the icon-rail CSS keys off.
    await page.evaluate(() => {
      const group = document.querySelector('[data-slot="sidebar"].group');
      group?.setAttribute("data-state", "collapsed");
      group?.setAttribute("data-collapsible", "icon");
    });
    await expect(sidebar).toHaveCSS("width", "48px");

    const rail = await sidebar.boundingBox();
    const active = sidebar.locator('[data-sidebar="menu-button"][data-active="true"]');
    const bar = await active.evaluate((el) => {
      const before = getComputedStyle(el, "::before");
      const item = el.closest('[data-sidebar="menu-item"]')!.getBoundingClientRect();
      return {
        width: before.width,
        color: before.backgroundColor,
        left: item.left + parseFloat(before.left),
        top: item.top + parseFloat(before.top),
      };
    });
    expect(bar.width).toBe("3px");
    expect(bar.color).toBe(SIDEBAR_CYAN);
    expect(bar.left).toBeGreaterThanOrEqual(rail!.x - 1);
    expect(bar.left + 3).toBeLessThanOrEqual(rail!.x + rail!.width + 1);
    expect(bar.top).toBeGreaterThanOrEqual(rail!.y - 1);
    expect(await sidebarPaint(page).then((p) => p.background)).toBe(NAVY_LIGHT);
  });
});

// --- dashboard KPI tiles --------------------------------------------------------

async function mockDashboardStats(page: Page, availability24h: number) {
  const checks = [
    { uid: "71111111-1111-1111-1111-111111111111", name: "Tile Check", type: "http", enabled: true, status: "up", lastResult: { status: "up", durationMs: 120 } },
  ];
  await page.route("**/api/v1/orgs/*/checks/stats*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        total: 1,
        enabled: 1,
        disabled: 0,
        byStatus: { created: 0, up: 1, down: 0, validating: 0, degraded: 0, warning: 0, unknown: 0 },
        down: 0,
        hardDown: 0,
        availability24h,
      }),
    }),
  );
  await page.route("**/api/v1/orgs/*/checks*", (route) => {
    if (!route.request().url().includes("/checks")) return route.continue();
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: checks, pagination: { total: 1 } }),
    });
  });
  for (const path of ["incidents", "results", "events"]) {
    await page.route(`**/api/v1/orgs/*/${path}*`, (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [], pagination: { total: 0 } }),
      }),
    );
  }
}

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

// Tailwind always composes five box-shadow layers (inset, inset ring, ring
// offset, ring, shadow); the unused ones are fully transparent.
function visibleLayers(shadow: string): string[] {
  return shadow
    .split(/,(?![^(]*\))/)
    .map((layer) => layer.trim())
    .filter((layer) => !layer.startsWith("rgba(0, 0, 0, 0) "));
}

const SHADOW_XS = "rgba(0, 0, 0, 0.05) 0px 1px 2px 0px";

test.describe("dashboard KPI tiles", () => {
  for (const theme of ["light", "dark"] as const) {
    test(`every tile shares one card, the tone colors the chip and the wash, the tier is a Badge (${theme})`, async ({
      authenticatedPage,
    }) => {
      const page = authenticatedPage;
      await useTheme(page, theme);
      await mockDashboardStats(page, 92);
      await page.goto("orgs/test");

      const tiles = page.locator('[data-slot="kpi-tile"]');
      await expect(tiles).toHaveCount(4, { timeout: 10000 });
      // No hero: no tile carries a variant any more.
      await expect(page.locator('[data-slot="kpi-tile"][data-variant]')).toHaveCount(0);

      const looks = await tiles.evaluateAll((els) =>
        els.map((el) => {
          const cs = getComputedStyle(el);
          const label = getComputedStyle(el.querySelector('[data-slot="kpi-tile-label"]')!);
          const value = getComputedStyle(el.querySelector('[data-slot="kpi-tile-value"]')!);
          return {
            surface: {
              background: cs.backgroundColor,
              image: cs.backgroundImage,
              borderWidth: cs.borderTopWidth,
              borderColor: cs.borderTopColor,
              shadow: cs.boxShadow,
              radius: cs.borderTopLeftRadius,
              translate: cs.translate,
            },
            label: {
              color: label.color,
              size: label.fontSize,
              weight: label.fontWeight,
              transform: label.textTransform,
            },
            valueWeight: value.fontWeight,
          };
        }),
      );
      // Same surface, label and value weight on all four tiles: the tone lives
      // on the chip and the wash, never on the card itself.
      for (const look of looks.slice(1)) expect(look).toEqual(looks[0]);

      const [first] = looks;
      expect(first.surface.background).toBe(await probe(page, "bg-card"));
      expect(first.surface.borderWidth).toBe("1px");
      expect(first.surface.radius).toBe("14px");
      expect(first.surface.translate).toBe("none");
      // Light: no background image at all. Dark: only Card's own faint
      // top-lit wash, shared by every card (checked equal above).
      if (theme === "light") expect(first.surface.image).toBe("none");
      expect(first.label).toEqual({
        color: await probe(page, "text-muted-foreground", "color"),
        size: "14px",
        weight: "500",
        transform: "none",
      });
      expect(first.valueWeight).toBe("600");

      // The tone: a plain count is primary, the 92% availability is the
      // "down" tier (error), zero down and zero incidents stay neutral.
      const TONES = [
        ["kpi-tile-monitored", "primary", "bg-primary/10", "text-primary", "from-primary/10"],
        ["kpi-tile-availability", "error", "bg-status-error/15", "text-status-error-foreground", "from-status-error/12"],
        ["kpi-tile-down", "neutral", "bg-muted", "text-muted-foreground", null],
        ["kpi-tile-incidents", "neutral", "bg-muted", "text-muted-foreground", null],
      ] as const;
      for (const [testId, tone, chipBg, chipText, washFrom] of TONES) {
        const tile = page.getByTestId(testId).locator('[data-slot="kpi-tile"]');
        await expect(tile, testId).toHaveAttribute("data-tone", tone);
        const chip = tile.locator('[data-slot="kpi-tile-icon"]');
        await expect(chip, testId).toHaveCSS("background-color", await probe(page, chipBg));
        await expect(chip, testId).toHaveCSS("color", await probe(page, chipText, "color"));
        const wash = tile.locator('[data-slot="kpi-tile-wash"]');
        if (washFrom === null) {
          await expect(wash, testId).toHaveCount(0);
          continue;
        }
        // A faint tint fading to transparent from the chip's corner, behind
        // the content and never in the way of the tile's link.
        await expect(wash, testId).toHaveCSS(
          "background-image",
          await probe(page, `bg-linear-to-bl ${washFrom} to-transparent to-60%`, "backgroundImage"),
        );
        await expect(wash, testId).toHaveCSS("background-image", /^linear-gradient\(.*rgba\(0, 0, 0, 0\) 60%\)$/);
        await expect(wash, testId).toHaveCSS("pointer-events", "none");
        await expect(wash, testId).toHaveCSS("position", "absolute");
      }

      // Color goes on the value only: a 92% availability is the "down" tier.
      const availability = page.getByTestId("kpi-tile-availability");
      await expect(availability.locator('[data-slot="kpi-tile-value"]')).toHaveCSS(
        "color",
        await probe(page, "text-status-error-foreground", "color"),
      );
      await expect(
        page.getByTestId("kpi-tile-monitored").locator('[data-slot="kpi-tile-value"]'),
      ).toHaveCSS("color", await probe(page, "text-foreground", "color"));

      // The tier word is a destructive status Badge, not a solid white chip.
      const badge = page.getByTestId("kpi-availability-badge");
      await expect(badge).toHaveAttribute("data-tier", "down");
      await expect(badge).toContainText("Down");
      await expect(badge).toHaveCSS("background-image", "none");
      await expect(badge).toHaveCSS("background-color", await probe(page, "bg-status-error/15"));
      await expect(badge).toHaveCSS("color", await probe(page, "text-status-error-foreground", "color"));
      await expect(badge).not.toHaveCSS("background-color", "rgb(255, 255, 255)");

      await useTheme(page, "light");
    });
  }

  test("hover deepens a tile's shadow and never moves it", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await useTheme(page, "light");
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await mockDashboardStats(page, 99.95);
    await page.goto("orgs/test");

    const tile = page.getByTestId("kpi-tile-monitored").locator('[data-slot="kpi-tile"]');
    await expect(tile).toBeVisible({ timeout: 10000 });
    await tile.scrollIntoViewIfNeeded();
    await page.mouse.move(0, 0);
    const shadow = () => tile.evaluate((el) => getComputedStyle(el).boxShadow);
    const rest = await shadow();
    const restBox = (await tile.boundingBox())!;

    await tile.hover();
    // transition-shadow animates it in: poll for the settled shadow-card-hover
    // (its hairline plus a deeper neutral drop).
    await expect
      .poll(shadow, { timeout: 3000 })
      .toContain("rgba(15, 23, 42, 0.12) 0px 6px 16px -6px");
    expect(await shadow()).not.toBe(rest);
    expect(await shadow()).toContain("rgba(15, 23, 42, 0.05) 0px 1px 2px 0px");
    await expect(tile).toHaveCSS("translate", "none");
    await expect(tile).toHaveCSS("transform", "none");
    expect((await tile.boundingBox())!.y).toBe(restBox.y);
  });

  test("at 375px the tiles sit two per row inside a 16px gutter and the sheet sidebar is navy", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await useTheme(page, "light");
    await page.setViewportSize({ width: 375, height: 812 });
    await mockDashboardStats(page, 99.95);
    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const monitored = page.getByTestId("kpi-tile-monitored");
    const availability = page.getByTestId("kpi-tile-availability");
    const down = page.getByTestId("kpi-tile-down");
    await expect(availability).toBeVisible({ timeout: 10000 });
    const a = (await monitored.boundingBox())!;
    const b = (await availability.boundingBox())!;
    const c = (await down.boundingBox())!;
    // Two columns: the first two tiles share a row and a width …
    expect(Math.abs(a.y - b.y)).toBeLessThanOrEqual(1);
    expect(Math.abs(a.width - b.width)).toBeLessThanOrEqual(1);
    expect(b.x).toBeGreaterThanOrEqual(a.x + a.width);
    // … and the third wraps under the first.
    expect(Math.abs(c.x - a.x)).toBeLessThanOrEqual(1);
    expect(c.y).toBeGreaterThanOrEqual(a.y + a.height);
    // A 16px gutter on each side.
    expect(Math.abs(a.x - 16)).toBeLessThanOrEqual(1);
    expect(Math.abs(b.x + b.width - (375 - 16))).toBeLessThanOrEqual(1);
    // No horizontal page scroll.
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(375);
    // The page title steps down to text-xl on a phone.
    await expect(page.getByRole("heading", { level: 1 })).toHaveCSS("font-size", "20px");

    // The mobile sheet: portaled to <body>, so it carries its own dark scope.
    await page.getByTestId("sidebar-trigger").click();
    const sheet = page.locator('[data-slot="sidebar"][data-mobile="true"]');
    await expect(sheet).toBeVisible();
    await expect(sheet).toHaveClass(/(^|\s)dark(\s|$)/);
    await expect(sheet).toHaveCSS("background-color", NAVY_LIGHT);
    await expect(sheet).toHaveCSS("background-image", /linear-gradient/);
    await expect(sheet).toHaveCSS("border-right-color", SIDEBAR_BORDER_LIGHT);
    expect(
      await sheet.evaluate((el) => {
        const probe = document.createElement("span");
        probe.style.color = "var(--sidebar)";
        el.appendChild(probe);
        const color = getComputedStyle(probe).color;
        probe.remove();
        return color;
      }),
    ).toBe(NAVY_LIGHT);
    await expect(sheet.locator('[data-sidebar="group-label"]').first()).toHaveCSS(
      "color",
      SIDEBAR_MUTED_FG,
    );
    await expect(
      sheet.locator('[data-sidebar="menu-button"][data-active="true"]'),
    ).toHaveCSS("background-image", /linear-gradient/);
    // The page behind stays light.
    expect(await page.evaluate(() => getComputedStyle(document.body).backgroundColor)).toBe(
      PAGE_BG_LIGHT,
    );
  });
});

// --- page header ----------------------------------------------------------------

test.describe("page header tile", () => {
  test("brand tone by default: the primary gradient, a white icon, no glow, semibold title", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await useTheme(page, "light");
    await page.goto("orgs/test/checks");

    const tile = page.locator('[data-slot="page-header-tile"]').first();
    await expect(tile).toHaveAttribute("data-tone", "brand");
    const gradient = await probe(page, "bg-primary-gradient", "backgroundImage");
    expect(gradient).toContain("linear-gradient");
    await expect(tile).toHaveCSS("background-image", gradient);
    await expect(tile).toHaveCSS("background-color", await probe(page, "bg-primary-fill"));
    await expect(tile).toHaveCSS("color", await probe(page, "text-primary-fill-foreground", "color"));
    await expect(tile).toHaveCSS("border-top-width", "0px");
    // The hairline drop only: no tinted glow under the tile.
    expect(visibleLayers(await tile.evaluate((el) => getComputedStyle(el).boxShadow))).toEqual([
      SHADOW_XS,
    ]);
    await expect(tile).toHaveCSS("border-top-left-radius", "10px");
    await expect(tile).toHaveCSS("width", "36px");
    await expect(tile).toHaveCSS("height", "36px");
    const h1 = page.getByRole("heading", { level: 1 });
    await expect(h1).toHaveCSS("font-weight", "600");
    await expect(h1).toHaveCSS("font-size", "24px");
  });

  test("the integration detail page shows the provider logo on the neutral tile", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await useTheme(page, "light");
    const uid = "72222222-2222-2222-2222-222222222222";
    await page.route(`**/api/v1/orgs/test/integrations/${uid}`, (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          uid,
          type: "webhook",
          name: "Acme webhook",
          enabled: true,
          isDefault: false,
          settings: { url: "https://hooks.acme.com/solidping" },
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
        }),
      });
    });
    await page.goto(`orgs/test/integrations/${uid}`);

    await expect(page.getByRole("heading", { level: 1, name: "Acme webhook" })).toBeVisible();
    const tile = page.locator('[data-slot="page-header-tile"]').first();
    await expect(tile).toHaveAttribute("data-tone", "neutral");
    await expect(tile).toHaveCSS("background-image", "none");
    await expect(tile).toHaveCSS("background-color", "oklch(0.962 0.01 255)");
  });
});

// --- page glow ------------------------------------------------------------------

test.describe("org app shell", () => {
  test("paints no glow in the content area, and only the primary gradient or washes that fade out, on every org route", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await useTheme(page, "light");
    const primaryGradient = await probe(page, "bg-primary-gradient", "backgroundImage");

    // Every element of the content column (the sidebar is a sibling, outside
    // it) whose computed background-image is a gradient, minus the allowed
    // ones: the primary gradient itself, and a LINEAR wash whose last stop is
    // transparent (a tint fading out). A radial glow, or a gradient that ends
    // on a color (the old blue-to-violet strips), is a violation.
    const scan = () =>
      page.evaluate((allowed) => {
        const main = document.querySelector('[data-slot="sidebar-inset"]');
        if (!main) throw new Error("no sidebar-inset");
        const painted = [main, ...main.querySelectorAll("*")]
          .map((el) => ({ el, image: getComputedStyle(el).backgroundImage }))
          .filter(({ image }) => image.includes("gradient"));
        const isWash = (image: string) =>
          /^linear-gradient\(.*rgba\(0, 0, 0, 0\)( \d+%)?\)$/.test(image);
        return {
          primary: painted.filter(({ image }) => image === allowed).length,
          violations: painted
            .filter(({ image }) => image !== allowed && !isWash(image))
            .map(({ el, image }) => `${image} :: ${el.outerHTML.slice(0, 120)}`),
        };
      }, primaryGradient);

    for (const route of ["orgs/test", "orgs/test/checks", "orgs/test/incidents"]) {
      await page.goto(route);
      await page.waitForLoadState("networkidle");
      await expect(page.getByRole("heading", { level: 1 }), route).toBeVisible();
      await expect(page.getByTestId("page-glow"), route).toHaveCount(0);
      const { primary, violations } = await scan();
      expect(violations, route).toEqual([]);
      // Positive control: the page-header tile paints the primary gradient.
      expect(primary, route).toBeGreaterThan(0);
    }

    // Positive controls: the same scan flags the glow utility (it still
    // exists, for the auth screens and /no-org) and a gradient that ends on
    // a color, but not a wash that fades out.
    await page.evaluate(() => {
      const host = document.querySelector('[data-slot="sidebar-inset"]')!;
      // Classes the app itself ships, so the build is sure to generate them.
      for (const cls of [
        "bg-page-glow",
        "bg-sidebar bg-sidebar-gradient",
        "bg-card bg-linear-to-r from-status-error/15 to-transparent to-70%",
      ]) {
        const el = document.createElement("div");
        el.className = cls;
        host.appendChild(el);
      }
    });
    const { violations } = await scan();
    expect(violations).toHaveLength(2);
    expect(violations.join("\n")).toContain("bg-page-glow");
    expect(violations.join("\n")).toContain("bg-sidebar-gradient");
  });
});
