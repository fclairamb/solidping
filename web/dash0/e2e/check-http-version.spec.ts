import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-10-03-01: the HTTP check form's Advanced section
// gained an "HTTP version" select (1.1 / 2 / 3). 1.1 is the default and is
// never written; any other value is saved as `httpVersion`, and the API
// rejects an unknown value.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

test.describe("HTTP check httpVersion", () => {
  test("defaults to 1.1, saves 2 when picked, and reloads it", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page.getByTestId("section-advanced-trigger").click();
    const select = page.getByTestId("check-http-version-select");
    await expect(select).toBeVisible();
    await expect(select).toContainText("HTTP/1.1");

    await page
      .getByTestId("check-name-input")
      .fill(`E2E HTTP version ${Date.now()}`);
    await page
      .getByTestId("check-url-input")
      .fill("https://example.com/http-version");

    await select.click();
    await page.getByRole("option", { name: "HTTP/2", exact: true }).click();
    await expect(select).toContainText("HTTP/2");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    expect(resp.status()).toBe(200);
    expect((await resp.json()).config.httpVersion).toBe("2");

    // The Advanced section auto-opens on the edit page: it holds a
    // non-default value.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-http-version-select")).toContainText(
      "HTTP/2",
    );

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("the API rejects an unknown httpVersion", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const resp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "http",
          name: `E2E HTTP version invalid ${Date.now()}`,
          config: { url: "https://example.com/", httpVersion: "http2" },
        },
      },
    );
    expect(resp.status()).toBe(422);
    const body = await resp.json();
    expect(body.code).toBe("VALIDATION_ERROR");
  });
});
