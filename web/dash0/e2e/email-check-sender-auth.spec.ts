import { test, expect, API_BASE, type Page } from "./fixtures";

// Spec 2026-11-12-03: the email check form's "Authentication checks" section
// persists the optional sender-authentication policy, and the server accepts
// and returns it.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

// The form hides its fields until the instance has an inbox domain.
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

test.describe("Email check sender authentication", () => {
  test("persists the authentication options set in the form", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await ensureEmailInboxConfigured(page, await getAuthToken(page));

    await page.goto("orgs/test/checks/new?checkType=email");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    await page.getByTestId("check-name-input").fill(`E2E Email Auth ${Date.now()}`);

    await page.getByTestId("email-auth-section").click();

    await page.getByTestId("email-require-spf-select").click();
    await page.getByRole("option", { name: "Softfail or better" }).click();
    await page.getByTestId("email-require-dkim-switch").click();
    await page.getByTestId("email-require-dmarc-select").click();
    await page.getByRole("option", { name: "Pass", exact: true }).click();
    await page.getByTestId("email-check-blocklists-switch").click();
    await page.getByTestId("email-blocklists-input").fill("zen.spamhaus.org\nbl.spamcop.net");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    // The stored config carries the policy.
    const token = await getAuthToken(page);
    const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const config = (await resp.json()).config;
    expect(config.require_spf).toBe("softfail");
    expect(config.require_dkim).toBe(true);
    expect(config.require_dmarc).toBe("pass");
    expect(config.check_blocklists).toBe(true);
    expect(config.blocklists).toEqual(["zen.spamhaus.org", "bl.spamcop.net"]);
    expect(typeof config.token).toBe("string");

    // And the edit form shows it back (the section opens by itself).
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await expect(page.getByTestId("email-require-spf-select")).toContainText("Softfail or better");
    await expect(page.getByTestId("email-require-dkim-switch")).toBeChecked();
    await expect(page.getByTestId("email-require-dmarc-select")).toContainText("Pass");
    await expect(page.getByTestId("email-check-blocklists-switch")).toBeChecked();
    await expect(page.getByTestId("email-blocklists-input")).toHaveValue(
      "zen.spamhaus.org\nbl.spamcop.net",
    );
  });
});
