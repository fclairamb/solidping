import { test, expect, type Page } from "./fixtures";

// Spec 2026-09-24-02 (electric identity, app chrome). The unit tests pin the
// class contract; these check what the browser actually paints: the navy
// sidebar in BOTH themes (desktop, icon rail, mobile sheet), the active
// marker, the page-header tile tones, the page glow and the hero KPI tile.

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
    const root = document.querySelector<HTMLElement>('[data-slot="sidebar"].group');
    const inner = document.querySelector<HTMLElement>('[data-slot="sidebar-inner"]');
    const container = document.querySelector<HTMLElement>('[data-slot="sidebar-container"]');
    const label = document.querySelector<HTMLElement>('[data-sidebar="group-label"]');
    const version = document.querySelector<HTMLElement>('[data-testid="server-version-text"]');
    if (!root || !inner || !container || !label) throw new Error("sidebar not rendered");
    return {
      rootIsDark: root.classList.contains("dark"),
      sidebarVar: getComputedStyle(root).getPropertyValue("--sidebar").trim(),
      innerSidebarVar: getComputedStyle(inner).getPropertyValue("--sidebar").trim(),
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

// --- dashboard hero tile ------------------------------------------------------

async function mockDashboardStats(page: Page, availability24h: number) {
  const checks = [
    { uid: "71111111-1111-1111-1111-111111111111", name: "Hero Check", type: "http", enabled: true, status: "up", lastResult: { status: "up", durationMs: 120 } },
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

test.describe("hero KPI tile", () => {
  for (const theme of ["light", "dark"] as const) {
    test(`the availability tile is the page's one hero, with a solid white tier chip (${theme})`, async ({
      authenticatedPage,
    }) => {
      const page = authenticatedPage;
      await useTheme(page, theme);
      await mockDashboardStats(page, 92);
      await page.goto("orgs/test");
      await page.waitForLoadState("networkidle");

      const hero = page.getByTestId("kpi-tile-availability").locator('[data-slot="kpi-tile"]');
      await expect(hero).toBeVisible({ timeout: 10000 });
      await expect(hero).toHaveAttribute("data-variant", "hero");
      await expect(page.locator('[data-slot="kpi-tile"][data-variant="hero"]')).toHaveCount(1);
      await expect(
        page.getByTestId("kpi-tile-monitored").locator('[data-slot="kpi-tile"]'),
      ).toHaveAttribute("data-variant", "default");

      await expect(hero).toHaveCSS("background-image", /linear-gradient\(135deg/);
      await expect(hero).toHaveCSS("background-size", "180% 180%");
      await expect(hero).toHaveCSS("background-position", "100% 100%");
      await expect(hero).toHaveCSS("border-top-width", "0px");
      await expect(hero).toHaveCSS("box-shadow", /0px 14px 28px -12px/);
      await expect(hero.locator('[data-slot="kpi-tile-value"]')).toHaveCSS("color", "oklch(1 0 0)");
      // 90% white (the unit test proves >= 4.5:1 over the cropped gradient).
      await expect(hero.locator('[data-slot="kpi-tile-label"]')).toHaveCSS("color", /\/ 0\.9\)$/);
      await expect(hero.locator('[data-slot="kpi-tile-sub"]')).toHaveCSS("color", /\/ 0\.9\)$/);

      // "Down" tier: a solid white chip with red-700 text, in BOTH themes.
      const badge = page.getByTestId("kpi-availability-badge");
      await expect(badge).toHaveAttribute("data-tier", "down");
      await expect(badge).toContainText("Down");
      await expect(badge).toHaveCSS("background-color", "rgb(255, 255, 255)");
      await expect(badge).toHaveCSS("color", "oklch(0.505 0.213 27.518)");

      await useTheme(page, "light");
    });
  }

  test("at 375px the hero stacks with the other tiles and the sheet sidebar is navy", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await useTheme(page, "light");
    await page.setViewportSize({ width: 375, height: 812 });
    await mockDashboardStats(page, 99.95);
    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");

    const monitored = page.getByTestId("kpi-tile-monitored");
    const hero = page.getByTestId("kpi-tile-availability");
    await expect(hero).toBeVisible({ timeout: 10000 });
    const a = (await monitored.boundingBox())!;
    const b = (await hero.boundingBox())!;
    // One column: same x and width, the hero below the first tile.
    expect(Math.abs(a.x - b.x)).toBeLessThanOrEqual(1);
    expect(Math.abs(a.width - b.width)).toBeLessThanOrEqual(1);
    expect(b.y).toBeGreaterThanOrEqual(a.y + a.height);
    expect(b.x + b.width).toBeLessThanOrEqual(375);
    // No horizontal page scroll.
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(375);

    // The mobile sheet: portaled to <body>, so it carries its own dark scope.
    await page.getByTestId("sidebar-trigger").click();
    const sheet = page.locator('[data-slot="sidebar"][data-mobile="true"]');
    await expect(sheet).toBeVisible();
    await expect(sheet).toHaveClass(/(^|\s)dark(\s|$)/);
    await expect(sheet).toHaveCSS("background-color", NAVY_LIGHT);
    await expect(sheet).toHaveCSS("background-image", /linear-gradient/);
    await expect(sheet).toHaveCSS("border-right-color", SIDEBAR_BORDER_LIGHT);
    expect(await sheet.evaluate((el) => getComputedStyle(el).getPropertyValue("--sidebar").trim())).toBe(
      NAVY_LIGHT,
    );
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
  test("brand tone by default: accent gradient, white icon, bold title", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await useTheme(page, "light");
    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    const tile = page.locator('[data-slot="page-header-tile"]').first();
    await expect(tile).toHaveAttribute("data-tone", "brand");
    await expect(tile).toHaveCSS("background-image", /linear-gradient/);
    await expect(tile).toHaveCSS("color", "oklch(1 0 0)");
    await expect(tile).toHaveCSS("border-top-left-radius", "10px");
    await expect(tile).toHaveCSS("box-shadow", /0px 8px 18px -8px/);
    const h1 = page.getByRole("heading", { level: 1 });
    await expect(h1).toHaveCSS("font-weight", "700");
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
    await page.waitForLoadState("networkidle");

    await expect(page.getByRole("heading", { level: 1, name: "Acme webhook" })).toBeVisible();
    const tile = page.locator('[data-slot="page-header-tile"]').first();
    await expect(tile).toHaveAttribute("data-tone", "neutral");
    await expect(tile).toHaveCSS("background-image", "none");
    await expect(tile).toHaveCSS("background-color", "oklch(0.962 0.01 255)");
  });
});

// --- page glow ------------------------------------------------------------------

test.describe("page glow", () => {
  test("shows on every org route, is not clickable, not printed, and shifts nothing", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await useTheme(page, "light");

    for (const route of ["orgs/test", "orgs/test/checks", "orgs/test/incidents"]) {
      await page.goto(route);
      await page.waitForLoadState("networkidle");
      const glow = page.getByTestId("page-glow");
      await expect(glow, route).toHaveCount(1);
      await expect(glow).toHaveAttribute("aria-hidden", "true");
      await expect(glow).toHaveCSS("pointer-events", "none");
      await expect(glow).toHaveCSS("position", "absolute");
      await expect(glow).toHaveCSS("height", "260px");
      await expect(glow).toHaveCSS("background-image", /radial-gradient/);
    }

    // Not clickable: the top-most element under a point inside the glow is
    // real content (the header's trigger), never the glow.
    const trigger = page.getByTestId("sidebar-trigger");
    const box = (await trigger.boundingBox())!;
    const hit = await page.evaluate(
      ([x, y]) => document.elementFromPoint(x, y)?.closest('[data-testid]')?.getAttribute("data-testid"),
      [box.x + box.width / 2, box.y + box.height / 2],
    );
    expect(hit).toBe("sidebar-trigger");
    const sidebarGroup = page.locator('[data-slot="sidebar"].group');
    await trigger.click();
    await expect(sidebarGroup).toHaveAttribute("data-state", "collapsed");
    await trigger.click();
    await expect(sidebarGroup).toHaveAttribute("data-state", "expanded");

    // No layout shift and no extra scroll: removing the glow moves nothing.
    const measure = () =>
      page.evaluate(() => ({
        h1: document.querySelector("h1")!.getBoundingClientRect().top,
        scroll: document.documentElement.scrollHeight,
      }));
    const withGlow = await measure();
    await page.getByTestId("page-glow").evaluate((el) => ((el as HTMLElement).style.display = "none"));
    const withoutGlow = await measure();
    expect(withGlow).toEqual(withoutGlow);
    await page.getByTestId("page-glow").evaluate((el) => ((el as HTMLElement).style.display = ""));

    // The glow paints BEHIND the header and content: they are positioned too
    // and come later in tree order.
    await expect(page.locator("main > header")).toHaveCSS("position", "relative");

    // Not printed.
    await page.emulateMedia({ media: "print" });
    await expect(page.getByTestId("page-glow")).toHaveCSS("display", "none");
    await page.emulateMedia({ media: "screen" });
    await expect(page.getByTestId("page-glow")).toHaveCSS("display", "block");
  });
});
