import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-09-30-05 (vnc check type): the form creates a vnc
// check with requireAuth on by default, the password is stored as a secret
// (never echoed back), and editing a screenshot check without re-entering the
// password still saves (the stored secret satisfies "screenshot needs a
// password").

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function getCheck(page: Page, token: string, uid: string) {
  const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(resp.status()).toBe(200);
  return await resp.json();
}

test.describe("VNC check", () => {
  test("creates a vnc check with a secret password and edits it without re-entering it", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=vnc");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // requireAuth is on by default.
    await expect(page.getByTestId("check-vnc-require-auth-checkbox")).toHaveAttribute(
      "data-state",
      "checked",
    );

    const checkName = `E2E VNC ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-host-input").fill("vnc.internal.example");
    await page.getByTestId("check-port-input").fill("5901");

    // A screenshot without a password is refused client-side.
    await page.getByTestId("check-vnc-screenshot-checkbox").click();
    await page.getByTestId("check-submit-button").click();
    await expect(page.getByText("Screenshot requires a password.").first()).toBeVisible();

    await page.getByTestId("check-vnc-password-input").fill("s3cret");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.type).toBe("vnc");
    expect(created.config.host).toBe("vnc.internal.example");
    expect(created.config.port).toBe(5901);
    expect(created.config.screenshot).toBe(true);
    expect(created.config.requireAuth ?? true).toBe(true);
    expect(created.config, "the password must never be echoed").not.toHaveProperty("password");
    expect(created.configPrivateKeys).toContain("password");

    // Edit: the password comes back as a placeholder, and an unrelated edit saves.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-vnc-password-input")).toHaveValue("");
    await expect(page.getByTestId("vnc-password-encrypted")).toBeVisible();

    await page.getByTestId("check-vnc-require-auth-checkbox").click();
    await page.getByTestId("check-host-input").fill("vnc2.internal.example");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f-]{36}$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    const edited = await getCheck(page, token, uid);
    expect(edited.config.host).toBe("vnc2.internal.example");
    expect(edited.config.requireAuth).toBe(false);
    expect(edited.configPrivateKeys, "the stored password survived the edit").toContain("password");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
