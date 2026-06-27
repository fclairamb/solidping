import { test, expect } from "./fixtures";

/**
 * Dashboard header refresh buttons must show a "Refresh" label from the `sm`
 * breakpoint (>= 640px) up and collapse to icon-only below it, while staying
 * accessible (aria-label) in both states. The label span is
 * `<span className="hidden sm:inline">`, so it is visible at desktop width and
 * hidden at mobile width; the button (and its aria-label) stays present either
 * way. This guards the responsive pattern applied across the dash0 pages.
 *
 * Default test locale is English, so the rendered label is "Refresh".
 */

const DESKTOP = { width: 1280, height: 800 };
const MOBILE = { width: 390, height: 812 };

test.describe("Header refresh button responsiveness", () => {
  test("canonical example on the design reference shows label at sm+, icon-only below", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/design-reference");
    await page.waitForLoadState("networkidle");

    // The dedicated "Header refresh button" example renders a live
    // outline button labelled "Refresh".
    const refresh = page
      .getByRole("button", { name: "Refresh", exact: true })
      .first();
    await refresh.waitFor({ state: "visible" });

    // ---- Desktop (>= sm): button is visible WITH its text label. ----
    await page.setViewportSize(DESKTOP);
    await expect(refresh).toBeVisible();
    await expect(refresh.getByText("Refresh")).toBeVisible();

    // ---- Mobile (< sm): button stays visible (icon-only); label hidden. ----
    await page.setViewportSize(MOBILE);
    await expect(refresh).toBeVisible();
    await expect(refresh.getByText("Refresh")).toBeHidden();

    // Accessible name is retained in the icon-only state.
    await expect(refresh).toHaveAccessibleName("Refresh");
  });

  test("integrations list refresh button shows label at sm+, icon-only below", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/integrations");
    await page.waitForLoadState("networkidle");

    const refresh = page.getByTestId("integrations-refresh");
    await refresh.waitFor({ state: "visible" });

    // The data-testid / onClick contract is preserved (button still present).
    await expect(refresh).toBeVisible();

    // ---- Desktop (>= sm): the "Refresh" label is visible. ----
    await page.setViewportSize(DESKTOP);
    await expect(refresh.getByText("Refresh")).toBeVisible();
    // Accessible even with the label shown.
    await expect(refresh).toHaveAccessibleName("Refresh");

    // ---- Mobile (< sm): the label hides; the button stays icon-only. ----
    await page.setViewportSize(MOBILE);
    await expect(refresh).toBeVisible();
    await expect(refresh.getByText("Refresh")).toBeHidden();
    // Still labelled for screen readers when icon-only.
    await expect(refresh).toHaveAccessibleName("Refresh");
  });
});
