import { test, expect, API_BASE, type Page } from "./fixtures";

// Spec 2026-11-18-03: the email check form persists max_delivery_delay, on
// create and on edit, and the inbound token survives the edit.

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

test.describe("Email check maximum delivery delay", () => {
  test("sets the delay on create, then changes it on edit", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    await ensureEmailInboxConfigured(page, token);
    const headers = { Authorization: `Bearer ${token}` };

    await page.goto("orgs/test/checks/new?checkType=email");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    await page.getByTestId("check-name-input").fill(`E2E Email Delay ${Date.now()}`);
    await page.getByTestId("email-max-delivery-delay-input").fill("10m");
    // The limit must be lower than the expected interval (the default is 5m).
    await page.getByTestId("check-period-input").fill("1");
    await page.getByTestId("check-period-unit-select").click();
    await page.getByRole("option", { name: "Hours" }).click();
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const readConfig = async () => {
      const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, { headers });
      return (await resp.json()).config;
    };

    let config = await readConfig();
    expect(config.max_delivery_delay).toBe("10m");
    const inboxToken = config.token;
    expect(typeof inboxToken).toBe("string");

    // The edit form shows it back after a reload; change it.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await expect(page.getByTestId("email-max-delivery-delay-input")).toHaveValue("10m");
    await page.getByTestId("email-max-delivery-delay-input").fill("15m");
    const patched = page.waitForResponse(
      (r) => r.request().method() === "PATCH" && r.url().includes(`/checks/${uid}`),
    );
    await page.getByTestId("check-submit-button").click();
    expect((await patched).status()).toBe(200);

    config = await readConfig();
    expect(config.max_delivery_delay).toBe("15m");
    expect(config.token).toBe(inboxToken);
  });
});
