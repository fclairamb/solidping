import { test, expect, type Page } from "./fixtures";

// Spec 2026-09-24-03 (electric identity, auth screens). The unit tests pin the
// class contract; these check what the browser actually paints: the aurora
// panel on the sidebar navy with a cyan accent on lg+, and the page glow plus
// the wordmark below lg, in both themes, on every auth page.

const NAVY_LIGHT = "oklch(0.18 0.05 263)";
const NAVY_DARK = "oklch(0.125 0.035 263)";
const AURORA_ACCENT = "oklch(0.85 0.12 220)";

const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 375, height: 812 };

// Every page that renders through AuthSplitLayout without a session.
// login shows the wordmark in its own card, so the layout does not repeat it.
const SPLIT_PAGES = [
  { name: "login", path: "orgs/test/login", layoutWordmark: false },
  { name: "register", path: "orgs/test/register", layoutWordmark: true },
  { name: "forgot password", path: "forgot-password", layoutWordmark: true },
  { name: "reset password", path: "reset-password/not-a-real-token", layoutWordmark: true },
  {
    name: "confirm registration",
    path: "confirm-registration/not-a-real-token",
    layoutWordmark: true,
  },
] as const;

async function openWithTheme(page: Page, path: string, theme: "light" | "dark") {
  await page.goto(path);
  await page.evaluate((t) => localStorage.setItem("theme", t), theme);
  await page.reload();
  await page.waitForLoadState("networkidle");
  await expect
    .poll(() => page.evaluate(() => document.documentElement.classList.contains("dark")))
    .toBe(theme === "dark");
}

async function formColumnGlow(page: Page) {
  const column = page.getByTestId("auth-form-column");
  await expect(column).toBeVisible();
  return column.evaluate((el) => getComputedStyle(el).backgroundImage);
}

async function hasHorizontalScroll(page: Page) {
  return page.evaluate(
    () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
  );
}

for (const theme of ["light", "dark"] as const) {
  test.describe(`auth screens, ${theme} theme`, () => {
    test(`lg+: the aurora panel sits on the sidebar navy with a cyan accent`, async ({ page }) => {
      await page.setViewportSize(DESKTOP);
      await openWithTheme(page, "orgs/test/login", theme);

      const panel = page.getByTestId("auth-aurora-panel");
      await expect(panel).toBeVisible();
      const paint = await panel.evaluate((el) => {
        const accent = el.querySelector('[data-testid="auth-aurora-accent"]');
        const checks = [...el.querySelectorAll("li svg")];
        return {
          background: getComputedStyle(el).backgroundColor,
          image: getComputedStyle(el).backgroundImage,
          accent: accent ? getComputedStyle(accent).color : null,
          checks: checks.map((svg) => getComputedStyle(svg).color),
          // Any crimson utility left anywhere in the panel's glow.
          brandClasses: [...el.querySelectorAll("*")]
            .map((n) => n.getAttribute("class") ?? "")
            .filter((c) => /\bbg-brand|\bfrom-brand|\bto-brand|\bvia-brand/.test(c)),
        };
      });
      expect(paint.background).toBe(theme === "dark" ? NAVY_DARK : NAVY_LIGHT);
      expect(paint.image).toContain("linear-gradient");
      expect(paint.accent).toBe(AURORA_ACCENT);
      expect(paint.checks.length).toBeGreaterThan(0);
      for (const color of paint.checks) expect(color).toBe(AURORA_ACCENT);
      expect(paint.brandClasses).toEqual([]);

      expect(await formColumnGlow(page)).toContain("radial-gradient");
      await expect(page.getByTestId("auth-mobile-wordmark")).toHaveCount(0);
      await page.screenshot({
        path: `test-results/screenshots/electric-auth-login-1440-${theme}.png`,
      });
    });

    for (const { name, path, layoutWordmark } of SPLIT_PAGES) {
      test(`375px: ${name} shows the glow and the wordmark, and fits the screen`, async ({
        page,
      }) => {
        await page.setViewportSize(PHONE);
        await openWithTheme(page, path, theme);

        await expect(page.getByTestId("auth-aurora-panel")).toBeHidden();
        expect(await formColumnGlow(page)).toContain("radial-gradient");
        if (layoutWordmark) {
          await expect(page.getByTestId("auth-mobile-wordmark")).toBeVisible();
        } else {
          // login: the card's own wordmark, never twice.
          await expect(page.getByTestId("auth-mobile-wordmark")).toHaveCount(0);
          await expect(page.getByTestId("login-logo")).toBeVisible();
        }
        expect(await hasHorizontalScroll(page)).toBe(false);
        if (name === "login") {
          await page.screenshot({
            path: `test-results/screenshots/electric-auth-login-375-${theme}.png`,
          });
        }
      });
    }

    test("375px: the 404 page is the navy aurora with the wordmark", async ({ page }) => {
      await page.setViewportSize(PHONE);
      await openWithTheme(page, "this-route-does-not-exist", theme);

      const panel = page.locator('[data-slot="aurora-panel"]');
      await expect(panel).toBeVisible();
      expect(await panel.evaluate((el) => getComputedStyle(el).backgroundColor)).toBe(
        theme === "dark" ? NAVY_DARK : NAVY_LIGHT,
      );
      await expect(panel.getByText("SolidPing", { exact: true })).toBeVisible();
      expect(await hasHorizontalScroll(page)).toBe(false);
    });
  });
}
