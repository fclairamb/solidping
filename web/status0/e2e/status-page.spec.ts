import { test, expect } from "@playwright/test";
import {
  API_BASE as BASE,
  STATUS_BASE,
  resolveDefaultStatusPage,
} from "./fixtures";

test.describe("Public status page", () => {
  test("org URL renders the default status page (not blank)", async ({
    page,
  }) => {
    // Resolved rather than hardcoded: `make dev` seeds `default`/`status-0`
    // and `SP_RUNMODE=test` seeds `test`/`test-status-page`, and this spec is
    // about the SPA mounting and naming its page — not about which fixture the
    // server happened to seed.
    const target = await resolveDefaultStatusPage();

    await page.goto(`${BASE}${STATUS_BASE}/${target.org}`);
    await page.waitForLoadState("networkidle");

    // React app must have mounted — root must not be empty
    const rootKids = await page.evaluate(
      () => document.getElementById("root")?.children?.length ?? 0,
    );
    expect(rootKids).toBeGreaterThan(0);

    // Should not show the 404 / not-found message
    await expect(page.getByText("Status Page Not Found")).not.toBeVisible();

    // Should show the status page name for the org's default page
    await expect(page.getByText(target.name).first()).toBeVisible({
      timeout: 10000,
    });

    await page.screenshot({
      path: "test-results/screenshots/status-page-default-org.png",
      fullPage: true,
    });
  });

  test("slug URL renders the named status page (not blank)", async ({
    page,
  }) => {
    const target = await resolveDefaultStatusPage();

    await page.goto(`${BASE}${STATUS_BASE}/${target.org}/${target.slug}`);
    await page.waitForLoadState("networkidle");

    // React app must have mounted — root must not be empty
    const rootKids = await page.evaluate(
      () => document.getElementById("root")?.children?.length ?? 0,
    );
    expect(rootKids).toBeGreaterThan(0);

    // Should not show the 404 / not-found message
    await expect(page.getByText("Status Page Not Found")).not.toBeVisible();

    // Should show the correct page name
    await expect(page.getByText(target.name).first()).toBeVisible({
      timeout: 10000,
    });

    await page.screenshot({
      path: "test-results/screenshots/status-page-slug.png",
      fullPage: true,
    });
  });

  test("root URL renders the index page", async ({ page }) => {
    await page.goto(`${BASE}${STATUS_BASE}/`);
    await page.waitForLoadState("networkidle");

    const rootKids = await page.evaluate(
      () => document.getElementById("root")?.children?.length ?? 0,
    );
    expect(rootKids).toBeGreaterThan(0);

    await page.screenshot({
      path: "test-results/screenshots/status-page-index.png",
      fullPage: true,
    });
  });
});
