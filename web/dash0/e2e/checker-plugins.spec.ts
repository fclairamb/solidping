import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-10-17-05 (checker plugins): the E2E server loads the
// repo's plugins/ folder (SP_PLUGINS_DIR), so the reference x-gopher type is
// offered, edited through the JSON fallback editor, checked against its
// schema, and saved only when valid.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function setConfig(page: Page, text: string) {
  const editor = page.getByTestId("plugin-config-editor").locator(".cm-content");
  await editor.click();
  await page.keyboard.press("ControlOrMeta+A");
  await page.keyboard.press("Delete");
  // insertText bypasses CodeMirror's bracket auto-closing.
  await page.keyboard.insertText(text);
}

test.describe("Checker plugins", () => {
  test("serves the plugin type and its schema", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const headers = { Authorization: `Bearer ${await getAuthToken(page)}` };

    const types = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/check-types`, { headers })
    ).json();
    const gopher = types.data.find((t: { type: string }) => t.type === "x-gopher");
    expect(gopher, "x-gopher is listed").toBeTruthy();
    expect(gopher.plugin).toEqual({ name: "gopher", version: "1.0.0" });
    expect(gopher.excludedFromTally).toBe(true);

    const schema = await page.request.get(`${API_BASE}/api/v1/checks/schema/x-gopher`);
    expect(schema.status()).toBe(200);
    expect((await schema.json()).required).toEqual(["host"]);

    // The org route the form reads (spec §18) serves the same document, and
    // 404s on a type nobody reported.
    const orgSchema = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/check-types/x-gopher/schema`,
      { headers },
    );
    expect(orgSchema.status()).toBe(200);
    expect((await orgSchema.json()).required).toEqual(["host"]);

    const unknown = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/check-types/x-nobody-reported/schema`,
      { headers },
    );
    expect(unknown.status()).toBe(404);
  });

  test("creates an x-gopher check through the JSON editor", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const headers = { Authorization: `Bearer ${await getAuthToken(page)}` };
    const name = `E2E gopher ${Date.now()}`;

    await page.goto("orgs/test/checks/new?checkType=x-gopher");
    await expect(page.getByTestId("plugin-config-editor")).toBeVisible();
    await page.getByTestId("check-name-input").fill(name);

    // A schema error is shown, and nothing is saved.
    await setConfig(page, '{"port": 70}');
    await expect(page.getByTestId("plugin-config-errors")).toContainText("host: is required");
    await page.getByTestId("check-submit-button").click();
    await expect(page).toHaveURL(/\/checks\/new/);

    const before = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks?q=${encodeURIComponent(name)}`, {
        headers,
      })
    ).json();
    expect(before.data).toHaveLength(0);

    // A valid config is saved.
    await setConfig(page, '{"host": "gopher.acme.com", "port": 70, "selector": "/docs"}');
    await expect(page.getByTestId("plugin-config-errors")).toHaveCount(0);
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await (
      await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, { headers })
    ).json();
    expect(created.type).toBe("x-gopher");
    expect(created.name).toBe(name);
    expect(created.config).toMatchObject({ host: "gopher.acme.com", port: 70, selector: "/docs" });

    // The server refuses a schema-invalid config on its own too, with the
    // same VALIDATION_ERROR (422) as a built-in config error.
    const refused = await page.request.patch(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers,
      data: { config: { host: "gopher.acme.com", port: 70000 } },
    });
    expect(refused.status()).toBe(422);
    expect((await refused.json()).code).toBe("VALIDATION_ERROR");
  });
});
