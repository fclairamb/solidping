import { test, expect, type Page } from "@playwright/test";
import { DASH_BASE } from "./fixtures";

/**
 * Sign in with Telegram (spec 2026-11-11-08).
 *
 * The E2E backend has no Telegram bot, so GET /api/v1/auth/providers is
 * answered with the real response plus a `telegram` entry: what is under test
 * here is the dashboard's side (the button, where it sends the browser, that
 * /register leaves it out, and how a refused callback reads). The server side
 * (hash check, freshness, account matching) is covered by the Go tests in
 * server/internal/handlers/auth/telegram_test.go.
 */

async function advertiseTelegram(page: Page): Promise<void> {
  await page.route("**/api/v1/auth/providers", async (route) => {
    const response = await route.fetch();
    const body = (await response.json()) as {
      data?: { name: string; type: string }[];
    };
    const data = (body.data ?? []).filter((p) => p.type !== "telegram");
    data.push({ name: "Telegram", type: "telegram" });
    await route.fulfill({ response, json: { ...body, data } });
  });
}

/** Stops the browser from leaving for oauth.telegram.org. */
async function interceptTelegramLogin(page: Page): Promise<string[]> {
  const requested: string[] = [];
  await page.route("**/api/v1/auth/telegram/login*", async (route) => {
    requested.push(route.request().url());
    await route.fulfill({
      status: 200,
      contentType: "text/html",
      body: "<html><body>telegram stub</body></html>",
    });
  });
  return requested;
}

test.describe("Login: Sign in with Telegram", () => {
  test("shows a Telegram button that starts the Telegram login for the org", async ({
    page,
  }) => {
    await advertiseTelegram(page);
    const requested = await interceptTelegramLogin(page);

    await page.goto("orgs/test/login");
    const button = page.getByTestId("login-oauth-telegram");
    await expect(button).toBeVisible();
    await expect(button).toHaveText("Telegram");
    await expect(button.locator("svg")).toBeVisible();

    await button.click();
    await expect(() => expect(requested.length).toBeGreaterThan(0)).toPass({
      timeout: 10000,
    });

    const requestURL = new URL(requested[0]);
    expect(requestURL.pathname).toBe("/api/v1/auth/telegram/login");
    expect(requestURL.searchParams.get("org")).toBe("test");
    expect(requestURL.searchParams.get("redirect_uri")).toBe(
      `${DASH_BASE}/orgs/test`,
    );
  });

  test("is not offered on /register: Telegram never creates an account", async ({
    page,
  }) => {
    await advertiseTelegram(page);

    // Positive control under the same mocked response.
    await page.goto("orgs/test/login");
    await expect(page.getByTestId("login-oauth-telegram")).toBeVisible();

    await page.goto("orgs/test/register");
    await expect(
      page.getByRole("button", { name: "Create account" }),
    ).toBeVisible();
    await expect(page.getByTestId("register-oauth-telegram")).toHaveCount(0);
  });

  test("an unlinked Telegram account lands on the login page with the reason", async ({
    page,
  }) => {
    await advertiseTelegram(page);

    const description =
      "This Telegram account is not linked to a SolidPing account. " +
      "Sign in another way, then connect Telegram in your account's notification settings.";
    await page.goto(
      `orgs/test/login?error=TELEGRAM_NOT_LINKED&error_description=${encodeURIComponent(description)}`,
    );

    await expect(page.getByTestId("login-error")).toContainText(
      "connect Telegram in your account",
    );
    await expect(page.getByTestId("login-oauth-telegram")).toBeVisible();
  });
});
