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

    // The refresh button only renders on the list view — an org with zero
    // connections shows the empty state instead, which has no refresh button.
    // Seed one webhook connection through the API so the list is on screen.
    // (Relative request URLs resolve against the configured baseURL origin.)
    const login = await page.request.post("/api/v1/auth/login", {
      data: { org: "test", email: "test@test.com", password: "test" },
    });
    const { accessToken } = await login.json();
    const created = await page.request.post("/api/v1/orgs/test/integrations", {
      headers: { Authorization: `Bearer ${accessToken}` },
      data: {
        type: "webhook",
        name: `E2E Refresh Seed ${Date.now()}`,
        settings: { url: "https://example.com/webhook" },
      },
    });
    expect(created.ok()).toBeTruthy();
    const connection = await created.json();

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

    // Clean up the seeded connection so later specs that assert on an empty
    // integrations org are unaffected.
    await page.request.delete(`/api/v1/orgs/test/integrations/${connection.uid}`, {
      headers: { Authorization: `Bearer ${accessToken}` },
    });
  });
});
