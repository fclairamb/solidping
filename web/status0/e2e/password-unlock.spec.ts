import { test, expect, type Page } from "@playwright/test";
import { API_BASE as BASE } from "./fixtures";

/**
 * Password-protected status pages (spec 2026-08-21-07) — the status0 half.
 *
 * The API is mocked rather than seeded: the point under test is the CLIENT's
 * branching on `401 STATUS_PAGE_LOCKED`, which is exactly the state a seeded
 * fixture cannot produce deterministically (it would need a real password page
 * and a real cookie jar per test). Mocking also lets the "wrong password"
 * branch be exercised without brute-forcing a real page and tripping the
 * server's rate limiter.
 *
 * The server-side rules — that a locked page really answers 401, that the
 * cookie unlocks it, and that `private` stays a 404 — are pinned in Go by
 * server/internal/handlers/statuspages/password_test.go.
 */
const ORG = "e2e-locked";
const SLUG = "locked";

function unlockedPayload() {
  return {
    uid: "22222222-2222-2222-2222-222222222222",
    name: "Locked Page",
    slug: SLUG,
    visibility: "password",
    hasPassword: true,
    isDefault: false,
    enabled: true,
    showAvailability: false,
    showResponseTime: false,
    historyDays: 0,
    historyPeriod: "24h",
    overallStatus: "operational",
    sections: [],
    availabilityThresholds: { thresholdUp: 99.9, thresholdDegraded: 99 },
  };
}

/**
 * Routes the page endpoint to 401 until `unlocked` flips, mirroring what the
 * real server does once the unlock cookie is set.
 */
async function mockLockedPage(page: Page, state: { unlocked: boolean }) {
  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}`, (route) => {
    if (state.unlocked) {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(unlockedPayload()),
      });
    }

    return route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({
        title: "This status page is password protected",
        code: "STATUS_PAGE_LOCKED",
      }),
    });
  });
}

test.describe("Public status page — password unlock", () => {
  test("a locked page shows the unlock form, not a not-found page", async ({
    page,
  }) => {
    await mockLockedPage(page, { unlocked: false });
    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("status-page-password-input")).toBeVisible();
    await expect(page.getByTestId("status-page-unlock-submit")).toBeVisible();

    // The distinction that matters: locked is NOT missing.
    await expect(page.locator("body")).not.toContainText(/not found/i);
    // ...and the page's content must not have leaked around the gate.
    await expect(page.locator("body")).not.toContainText("Locked Page");
  });

  test("a wrong password keeps the visitor on the form with an error", async ({
    page,
  }) => {
    const state = { unlocked: false };
    await mockLockedPage(page, state);
    await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}/unlock`, (route) =>
      route.fulfill({
        status: 401,
        contentType: "application/json",
        body: JSON.stringify({
          title: "Incorrect password",
          code: "STATUS_PAGE_LOCKED",
        }),
      }),
    );

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("status-page-password-input").fill("nope");
    await page.getByTestId("status-page-unlock-submit").click();

    await expect(page.getByTestId("status-page-password-error")).toHaveText(
      /incorrect password/i,
    );
    await expect(page.getByTestId("status-page-password-input")).toBeVisible();
  });

  test("the right password reveals the page", async ({ page }) => {
    const state = { unlocked: false };
    await mockLockedPage(page, state);
    await page.route(
      `**/api/v1/status-pages/${ORG}/${SLUG}/unlock`,
      (route) => {
        state.unlocked = true;

        return route.fulfill({ status: 204, body: "" });
      },
    );

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("status-page-password-input").fill("correct-horse");
    await page.getByTestId("status-page-unlock-submit").click();

    await expect(page.getByTestId("overall-status-badge")).toBeVisible();
    await expect(page.getByTestId("status-page-password-input")).toHaveCount(0);
  });

  test("a rate-limited attempt surfaces the server's message", async ({
    page,
  }) => {
    await mockLockedPage(page, { unlocked: false });
    await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}/unlock`, (route) =>
      route.fulfill({
        status: 429,
        contentType: "application/json",
        body: JSON.stringify({
          title: "Too many attempts. Please try again in a few minutes.",
          code: "RATE_LIMITED",
        }),
      }),
    );

    await page.goto(`${BASE}/status0/${ORG}/${SLUG}`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("status-page-password-input").fill("guess");
    await page.getByTestId("status-page-unlock-submit").click();

    await expect(page.getByTestId("status-page-password-error")).toHaveText(
      /too many attempts/i,
    );
  });
});

test.describe("Public status page — branding", () => {
  const BRAND_ORG = "e2e-branding";
  const BRAND_SLUG = "branded";

  function brandedPayload(overrides: Record<string, unknown>) {
    return {
      ...unlockedPayload(),
      slug: BRAND_SLUG,
      name: "Branded Page",
      visibility: "public",
      hasPassword: false,
      ...overrides,
    };
  }

  async function mockBranded(page: Page, overrides: Record<string, unknown>) {
    await page.route(
      `**/api/v1/status-pages/${BRAND_ORG}/${BRAND_SLUG}`,
      (route) =>
        route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(brandedPayload(overrides)),
        }),
    );
  }

  test("an uploaded logo replaces the SolidPing mark", async ({ page }) => {
    await mockBranded(page, {
      logoUrl: "/pub/status-page-assets/abc123",
    });
    await page.goto(`${BASE}/status0/${BRAND_ORG}/${BRAND_SLUG}`);
    await page.waitForLoadState("networkidle");

    const logo = page.getByTestId("status-page-logo");
    await expect(logo).toBeVisible();
    await expect(logo).toHaveAttribute(
      "src",
      "/pub/status-page-assets/abc123",
    );
  });

  test("hideBranding removes the powered-by footer, and the default keeps it", async ({
    page,
  }) => {
    // Positive control first: without the flag the badge is there, so the
    // absence asserted below is the flag's doing and not a broken selector.
    await mockBranded(page, {});
    await page.goto(`${BASE}/status0/${BRAND_ORG}/${BRAND_SLUG}`);
    await page.waitForLoadState("networkidle");
    await expect(page.locator(".sp-powered-by")).toHaveCount(1);

    await page.unrouteAll({ behavior: "ignoreErrors" });
    await mockBranded(page, { hideBranding: true });
    await page.goto(`${BASE}/status0/${BRAND_ORG}/${BRAND_SLUG}`);
    await page.waitForLoadState("networkidle");
    await expect(page.locator(".sp-powered-by")).toHaveCount(0);
  });
});
