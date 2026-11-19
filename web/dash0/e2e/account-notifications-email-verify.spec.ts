import { test, expect, API_BASE, uniqueStamp, type Page } from "./fixtures";

/**
 * Spec 2026-11-19-02: an email contact for an address other than the member's
 * own verified sign-in email starts unverified, is not testable, and confirms
 * an emailed 6-digit code through the same dialog as a phone number.
 *
 * Runs against the real server: the code is really issued and queued as an
 * email. The E2E server has no mailbox to read it back from, so the test
 * proves the round trip up to a wrong code being refused.
 *
 * It seeds a throwaway user and org: adding an email row to the shared `test`
 * org would break the specs that count its rows, which run in parallel.
 */
const PASSWORD = "Email-Verify-123!";

async function seedOwnOrg(page: Page): Promise<string> {
  const stamp = uniqueStamp();
  const email = `email-verify-${stamp}@unknown.example`;

  const created = await page.request.post(`${API_BASE}/api/v1/test/users`, {
    data: { email, password: PASSWORD, name: "Email Verify User" },
  });
  if (created.status() !== 201) {
    test.skip(true, `test user-seed endpoint unavailable (SP_RUNMODE=test?): ${created.status()}`);
  }

  const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { email, password: PASSWORD },
  });
  expect(loginResp.status()).toBe(200);
  const login = (await loginResp.json()) as { accessToken: string };

  const slug = `ev-${stamp}`;
  const orgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
    headers: { Authorization: `Bearer ${login.accessToken}` },
    data: { name: `Email Verify ${stamp}`, slug },
  });
  expect(orgResp.status()).toBe(201);
  const org = (await orgResp.json()) as { accessToken: string; refreshToken?: string; expiresIn?: number };

  await page.addInitScript(
    ({ accessToken, refreshToken, expiresIn, orgSlug }) => {
      localStorage.setItem("solidping_session_token", accessToken);
      if (refreshToken) localStorage.setItem("solidping_refresh_token", refreshToken);
      if (expiresIn) {
        localStorage.setItem("solidping_expires_at", String(Date.now() + expiresIn * 1000));
        localStorage.setItem("solidping_expires_in", String(expiresIn));
      }
      localStorage.setItem("solidping_org", orgSlug);
    },
    {
      accessToken: org.accessToken,
      refreshToken: org.refreshToken ?? "",
      expiresIn: org.expiresIn ?? 0,
      orgSlug: slug,
    },
  );

  return slug;
}

test.describe("Account Notifications — email verification", () => {
  test("a new email address must be confirmed before it is paged", async ({ page }) => {
    const slug = await seedOwnOrg(page);
    await page.goto(`orgs/${slug}/account/notifications`);

    const list = page.getByTestId("notification-routes-list");
    await expect(list).toBeVisible();

    const address = `oncall-${Date.now()}@acme.example`;
    await page.getByTestId("add-contact-button").click();
    await page.getByTestId("add-contact-type-email").click();
    await page.getByTestId("add-contact-input").fill(address);
    await page.getByTestId("add-contact-submit").click();

    const row = list.locator(
      `[data-testid="notification-route-row"][data-contact-value=${JSON.stringify(address)}]`,
    );
    await expect(row).toBeVisible();
    await expect(row.locator('[data-testid^="route-unverified-"]')).toBeVisible();
    await expect(row.locator('[data-testid^="test-route-"]')).toHaveCount(0);

    await row.locator('[data-testid^="verify-phone-"]').click();
    const dialog = page.getByTestId("verify-phone-dialog");
    await expect(dialog).toBeVisible();
    await expect(page.getByTestId("verify-phone-description")).toContainText(address);

    await page.getByTestId("verify-send-code").click();
    await expect(page.getByTestId("verify-code-input")).toBeVisible();

    await page.getByTestId("verify-code-input").fill("000000");
    await page.getByTestId("verify-confirm").click();
    await expect(dialog).toContainText(/incorrect/i);
    await expect(row.locator('[data-testid^="route-unverified-"]')).toBeVisible();

    // Removing the unconfirmed contact still works.
    await page.keyboard.press("Escape");
    await row.locator('[data-testid^="delete-contact-"]').click();
    await expect(row).toHaveCount(0);
  });
});
