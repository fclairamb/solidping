import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-07-21-02: the HTTP check form's single numeric
// "Expected Status" field was replaced with a TokenChipsInput of status
// patterns (exact codes or "NXX" wildcards), matching what the backend
// (checkhttp.MatchStatusCode / ExpectedStatusCodes) already supported.
// Modeled on check-http-basic-auth.spec.ts's create-through-the-real-form,
// then-reload-the-edit-page pattern.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function getCheck(page: Page, token: string, uid: string) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  expect(resp.status()).toBe(200);
  return await resp.json();
}

test.describe("HTTP check expected-status codes", () => {
  test("space-separated codes become two chips, persist on reload, and save as expectedStatusCodes", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Status Codes ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-url-input")
      .fill("https://example.com/status-codes");

    const codesField = page.getByTestId("check-expected-status-codes-input");
    await codesField.fill("200 4XX");
    await page.getByTestId("check-name-input").click(); // blur, commits the draft

    await expect(
      page.getByTestId("check-expected-status-codes-chip-0"),
    ).toContainText("200");
    await expect(
      page.getByTestId("check-expected-status-codes-chip-1"),
    ).toContainText("4XX");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config.expectedStatusCodes).toEqual(["200", "4XX"]);
    expect(created.config).not.toHaveProperty("expectedStatus");

    // Reload the edit page — both chips persist.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByTestId("check-expected-status-codes-chip-0"),
    ).toContainText("200");
    await expect(
      page.getByTestId("check-expected-status-codes-chip-1"),
    ).toContainText("4XX");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("a lowercase wildcard normalizes to uppercase on commit", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // A fresh form seeds the implicit ["200"] default as chip-0 — remove it
    // first so the only chip left is the one this test types.
    await page.getByTestId("check-expected-status-codes-chip-remove-0").click();

    const codesField = page.getByTestId("check-expected-status-codes-input");
    await codesField.fill("4xx");
    await codesField.press("Enter");

    await expect(
      page.getByTestId("check-expected-status-codes-chip-0"),
    ).toContainText("4XX");
  });

  test("an invalid pattern renders a destructive chip and blocks save", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Status Invalid ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-url-input")
      .fill("https://example.com/status-invalid");

    // Remove the seeded implicit ["200"] default so the only chip is the
    // invalid one this test exercises.
    await page.getByTestId("check-expected-status-codes-chip-remove-0").click();

    const codesField = page.getByTestId("check-expected-status-codes-input");
    await codesField.fill("6XX");
    await codesField.press("Enter");

    await expect(
      page.getByTestId("check-expected-status-codes-chip-0"),
    ).toContainText("6XX");
    await expect(
      page.getByTestId("check-expected-status-codes-error"),
    ).toBeVisible();

    const urlBefore = page.url();
    await page.getByTestId("check-submit-button").click();

    // The invalid chip blocks the save: the top-level error banner surfaces
    // the field error (handleSubmit returns before ever calling the create
    // mutation, so by the time this is visible no navigation was queued),
    // and the page never left the create form.
    await expect(page.getByRole("alert")).toContainText("6XX");
    expect(page.url()).toBe(urlBefore);

    // Removing the invalid chip and adding a valid one restores save-ability.
    await page.getByTestId("check-expected-status-codes-chip-remove-0").click();
    await expect(
      page.getByTestId("check-expected-status-codes-error"),
    ).toHaveCount(0);
    await codesField.fill("201");
    await codesField.press("Enter");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    expect(page.url()).not.toBe(urlBefore);
  });

  test("an existing legacy expectedStatus check migrates to expectedStatusCodes on next save", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          name: `E2E Legacy Status ${Date.now()}`,
          type: "http",
          config: {
            url: "https://example.com/legacy-status",
            expectedStatus: 201,
          },
        },
      },
    );
    expect(createResp.status()).toBe(201);
    const created = await createResp.json();
    const uid = created.uid;
    expect(created.config.expectedStatus).toBe(201);

    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByTestId("check-expected-status-codes-chip-0"),
    ).toContainText("201");
    await expect(
      page.getByTestId("check-expected-status-codes-chip-1"),
    ).toHaveCount(0);

    // Save without touching the field — the legacy key migrates automatically.
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f-]{36}$/, { timeout: 10000 });

    const saved = await getCheck(page, token, uid);
    expect(saved.config.expectedStatusCodes).toEqual(["201"]);
    expect(saved.config).not.toHaveProperty("expectedStatus");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("a plain-200 check saves with neither status key", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Status Default ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-url-input")
      .fill("https://example.com/status-default");

    // The chip list already seeds to ["200"] — leave it untouched.
    await expect(
      page.getByTestId("check-expected-status-codes-chip-0"),
    ).toContainText("200");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(page, token, uid);
    expect(created.config).not.toHaveProperty("expectedStatusCodes");
    expect(created.config).not.toHaveProperty("expectedStatus");

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
