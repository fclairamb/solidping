import { test, expect, API_BASE, mockSloCoverage, type Locator, type Page } from "./fixtures";

// Spec 2026-11-15-04: dash0 records PostHog sessions unmasked, so the
// heartbeat URL (its `?token=`) and the email check address (`<token>@…`)
// render under `ph-no-capture`, the copy buttons still copy them, and the
// checks list carries no `title` with the full address.

test.beforeEach(async ({ authenticatedPage }) => {
  await mockSloCoverage(authenticatedPage);
  await authenticatedPage.context().grantPermissions(["clipboard-read", "clipboard-write"]);
});

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function ensureEmailInboxConfigured(page: Page, token: string) {
  const resp = await page.request.put(`${API_BASE}/api/v1/system/parameters/email.inbox`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      value: {
        enabled: true,
        sessionUrl: "https://jmap.e2e-test.invalid/session",
        username: "e2e@e2e-test.invalid",
        password: "not-a-real-password",
        addressDomain: "inbox.e2e-test.invalid",
      },
    },
  });
  expect(resp.status()).toBe(200);
}

/** Create a check of the given type through the form, return its token. */
async function createCheck(page: Page, type: string, name: string): Promise<string> {
  await page.goto(`orgs/test/checks/new?checkType=${type}`);
  await expect(page.getByTestId("check-name-input")).toBeVisible();
  await page.getByTestId("check-name-input").fill(name);
  await page.getByTestId("check-submit-button").click();
  await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");
  const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];
  const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${await getAuthToken(page)}` },
  });
  const token = (await resp.json()).config?.token;
  expect(typeof token).toBe("string");
  return token as string;
}

function blocked(locator: Locator): Promise<boolean> {
  return locator.evaluate((el) => el.closest(".ph-no-capture") !== null);
}

test.describe("Check endpoints stay out of session replay", () => {
  test("heartbeat URL and curl command are blocked and still copied", async ({ authenticatedPage: page }) => {
    const name = `E2E HB NoCapture ${Date.now()}`;
    const token = await createCheck(page, "heartbeat", name);

    const url = page.getByTestId("heartbeat-url");
    await expect(url).toContainText(`?token=${token}`);
    expect(await blocked(url)).toBe(true);
    expect(await blocked(page.getByTestId("heartbeat-curl"))).toBe(true);
    expect(await blocked(page.getByRole("heading", { name }))).toBe(false);

    await page.getByTestId("heartbeat-url-copy").click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toContain(`?token=${token}`);
    await page.getByTestId("heartbeat-curl-copy").click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toMatch(/^curl "/);
  });

  test("email address is blocked, copied, and not in the list row's title", async ({ authenticatedPage: page }) => {
    await ensureEmailInboxConfigured(page, await getAuthToken(page));
    const name = `E2E Email NoCapture ${Date.now()}`;
    const token = await createCheck(page, "email", name);

    const address = page.getByTestId("email-check-address");
    await expect(address).toHaveText(`${token}@inbox.e2e-test.invalid`);
    expect(await blocked(address)).toBe(true);
    expect(await blocked(page.getByTestId("email-check-send-test"))).toBe(true);

    await page.getByTestId("email-check-copy-btn").click();
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(`${token}@inbox.e2e-test.invalid`);

    await page.goto("orgs/test/checks");
    await page.getByTestId("checks-search-input").fill(name);
    const row = page.getByTestId("check-row").filter({ hasText: name });
    const target = row.getByTestId("check-row-email-target");
    await expect(target).toHaveText(`${token.slice(0, 8)}…@inbox.e2e-test.invalid`);
    expect(await row.evaluate((el, t) => el.outerHTML.includes(t), token)).toBe(false);
  });
});
