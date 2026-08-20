import { test, expect, API_BASE, type Page } from "./fixtures";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function deleteConnection(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/integrations/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe("Matrix channel", () => {
  test("create via the form, list with icon/label, edit, and delete", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // 1. Create via the picker + form.
    await page.goto("orgs/test/integrations/new?type=matrix");
    await page.waitForLoadState("networkidle");

    const name = `E2E Matrix ${Date.now()}`;
    await page.getByLabel("Name").fill(name);
    await page.getByLabel(/homeserver url/i).fill("https://matrix.org");
    await page.locator("#ch-matrix-token").fill("syt_e2e_secret_token");
    await page.getByLabel(/^room/i).fill("!abcdef:matrix.org");

    await page.getByRole("button", { name: /create integration/i }).click();
    await page.waitForURL((url) =>
      /\/integrations\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(
        url.pathname,
      ),
    );
    const uid = page.url().split("/").pop()!;

    // 2. Lists with icon/label on the integrations index.
    await page.goto("orgs/test/integrations");
    await page.waitForLoadState("networkidle");
    await expect(page.getByText(name)).toBeVisible({ timeout: 15000 });
    await expect(page.getByText("Matrix", { exact: true }).first()).toBeVisible();

    // 3. Edit: the homeserver URL and room persist; the access token is
    // never echoed back (it round-trips server-side, see the Go
    // integration test covering the encrypted-envelope round trip).
    await page.goto(`orgs/test/integrations/${uid}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByLabel(/homeserver url/i)).toHaveValue(
      "https://matrix.org",
    );
    await expect(page.getByLabel(/^room/i)).toHaveValue("!abcdef:matrix.org");
    await expect(page.locator("#ch-matrix-token")).toHaveValue("");

    await page.getByLabel(/^room/i).fill("!updated:matrix.org");
    await page.getByTestId("integration-save").click();
    await page.waitForLoadState("networkidle");

    await page.goto(`orgs/test/integrations/${uid}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByLabel(/^room/i)).toHaveValue("!updated:matrix.org");

    // 4. Delete via the UI.
    await page.getByRole("button", { name: /delete integration/i }).click();
    const dialog = page.getByRole("alertdialog");
    await expect(dialog).toBeVisible();
    await dialog.getByRole("button", { name: /delete/i }).click();

    await page.waitForURL((url) => url.pathname.endsWith("/integrations"));
    await expect(page.getByText(name)).toHaveCount(0);

    // Cleanup in case the UI delete assertion above ever regresses.
    await deleteConnection(page, token, uid);
  });

  test("send-test uses the registry for free (mocked delivery)", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const name = `E2E Matrix Test ${Date.now()}`;
    const resp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/integrations`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          type: "matrix",
          name,
          settings: {
            homeserverUrl: "https://matrix.org",
            accessToken: "syt_e2e_secret_token",
            roomId: "!abcdef:matrix.org",
          },
        },
      },
    );
    const integration = await resp.json();

    await page.route(
      `**/api/v1/orgs/test/integrations/${integration.uid}/test`,
      async (route) => {
        if (route.request().method() === "POST") {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({
              success: true,
              statusCode: 200,
              durationMs: 18,
            }),
          });
          return;
        }
        await route.continue();
      },
    );

    await page.goto(`orgs/test/integrations/${integration.uid}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("integration-test-section")).toBeVisible();
    await page.getByTestId("webhook-send-test").click();

    const badge = page.getByTestId("webhook-test-result");
    await expect(badge).toBeVisible();
    await expect(badge).toContainText("200 OK");

    await deleteConnection(page, token, integration.uid);
  });
});
