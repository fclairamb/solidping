import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for issue #1000 (unix_socket check type): the form creates a
// unix_socket check in HTTP mode, the path and the HTTP expectations are saved,
// and the edit form reopens it in the same mode.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

test.describe("Unix socket check", () => {
  test("creates a unix_socket check from the form and saves its path", async ({
    authenticatedPage,
  }) => {
    test.setTimeout(90_000);
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=unix_socket");
    await expect(page.getByTestId("check-socket-path-input")).toBeVisible();

    // The path is required.
    await page.getByTestId("check-name-input").fill(`E2E unix socket ${Date.now()}`);
    await page.getByTestId("check-submit-button").click();
    await expect(page.getByTestId("check-form-error")).toContainText("Socket path is required");

    await page.getByTestId("check-socket-path-input").fill("/run/acme/app.sock");
    await page.getByTestId("check-socket-mode-select").click();
    await page.getByTestId("check-socket-mode-http").click();
    await page.getByTestId("check-socket-http-path-input").fill("/healthcheck");
    await page.getByTestId("check-socket-expected-status-input").fill("204");
    await page.getByTestId("check-socket-body-expect-input").fill("ok");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const token = await getAuthToken(page);
    const created = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
    ).json();
    expect(created.type).toBe("unix_socket");
    expect(created.config.path).toBe("/run/acme/app.sock");
    expect(created.config.http_path).toBe("/healthcheck");
    expect(created.config.expected_status).toBe(204);
    expect(created.config.body_expect).toBe("ok");

    // The edit form reopens in HTTP mode with the saved values.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await expect(page.getByTestId("check-socket-path-input")).toHaveValue("/run/acme/app.sock");
    await expect(page.getByTestId("check-socket-http-path-input")).toHaveValue("/healthcheck");
  });
});
