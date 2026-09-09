import { test, expect } from "@playwright/test";
import {
  API_BASE as BASE,
  STATUS_BASE,
  resolveDefaultStatusPage,
} from "./fixtures";

test.describe("Status page subscribe widget", () => {
  test("subscribe widget submits and shows check-your-inbox state", async ({
    page,
  }) => {
    // Resolved, not hardcoded — `default`/`status-0` only exists on a
    // `make dev` server, while CI runs `SP_RUNMODE=test` (`test` /
    // `test-status-page`). See resolveDefaultStatusPage.
    const target = await resolveDefaultStatusPage();

    await page.goto(`${BASE}${STATUS_BASE}/${target.org}/${target.slug}`);
    await page.waitForLoadState("networkidle");

    // Widget heading is visible.
    await expect(page.getByText("Subscribe to updates")).toBeVisible({
      timeout: 10000,
    });

    // The RSS/Atom feed link points at feed.xml.
    const feedLink = page.getByRole("link", { name: /RSS/i });
    await expect(feedLink).toBeVisible();
    await expect(feedLink).toHaveAttribute("href", /feed\.xml$/);

    // Fill the email and submit.
    const email = `e2e-${Date.now()}@example.com`;
    await page.getByLabel("Email address").fill(email);
    await page.getByRole("button", { name: "Subscribe" }).click();

    // Confirmation state appears.
    await expect(page.getByText(/Check your inbox/i)).toBeVisible({
      timeout: 10000,
    });

    await page.screenshot({
      path: "test-results/screenshots/subscribe-widget-confirm.png",
      fullPage: true,
    });
  });

  test("feed.xml endpoint returns Atom XML", async ({ request }) => {
    const target = await resolveDefaultStatusPage();

    const res = await request.get(
      `${BASE}/api/v1/status-pages/${target.org}/${target.slug}/feed.xml`,
    );
    expect(res.status()).toBe(200);
    expect(res.headers()["content-type"]).toContain("application/atom+xml");

    const body = await res.text();
    expect(body).toContain("<feed");
  });
});
