import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-09-11-01: opening an API-created HTTP check in the
// dashboard and pressing Save without changing anything used to DELETE every
// config key the form did not model — the request `body`, the plain `headers`,
// `body_expect`, `headers_pattern`, and a snake-spelled `expected_status`.
// Nothing in the UI ever said so; the check simply started failing.
//
// Every assertion here is on the config read back from the API after a real
// save. Asserting on client state would pass on `main`, because the bug is
// that the form's state never carried those keys in the first place.
//
// Modeled on check-http-json-assertions.spec.ts.

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

async function deleteCheck(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

// createViaApi builds the kind of check only the API/CLI/manifest can build
// today: a POST probe with a body, plain headers and response assertions.
async function createViaApi(
  page: Page,
  token: string,
  label: string,
  config: Record<string, unknown>,
  period = "00:05:00",
): Promise<string> {
  const stamp = Date.now();
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name: `E2E HTTP ${label} ${stamp}`,
      slug: `e2e-http-${label}-${stamp}`,
      type: "http",
      period,
      config,
    },
  });
  expect(resp.status()).toBe(201);
  return (await resp.json()).uid;
}

// effectiveExpectedStatus reads whichever spelling the server re-emits, so the
// test pins the check's actual contract rather than a key name.
function effectiveExpectedStatus(config: Record<string, unknown>): string[] {
  const codes = config.expectedStatusCodes ?? config.expected_status_codes;
  if (Array.isArray(codes)) return codes.map(String);
  const single = config.expectedStatus ?? config.expected_status;
  if (single !== undefined && single !== null) return [String(single)];
  // Neither key stored: 200 is the implicit default.
  return ["200"];
}

async function saveAndWait(page: Page, uid: string) {
  await page.getByTestId("check-submit-button").click();
  await page.waitForURL(`**/checks/${uid}`, { timeout: 10000 });
}

test.describe("HTTP check request body and headers", () => {
  test("saving an untouched API-created check destroys nothing", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const config = {
      url: "https://acme.com/realms/acme/protocol/openid-connect/token",
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: "grant_type=password&client_id=admin-cli&scope=openid",
      expected_status: 200,
      followRedirects: false,
      body_expect: "access_token",
      headers_pattern: { "content-type": "^application/json" },
    };
    const uid = await createViaApi(page, token, "untouched", config);

    try {
      await page.goto(`orgs/test/checks/${uid}/edit`);
      await page.waitForLoadState("networkidle");
      await expect(page.getByTestId("check-url-input")).toHaveValue(config.url);

      // Save without touching a single field — the reported scenario.
      await saveAndWait(page, uid);

      const saved = await getCheck(page, token, uid);
      expect(saved.config.body).toBe(config.body);
      expect(saved.config.headers).toEqual(config.headers);
      expect(saved.config.body_expect).toBe(config.body_expect);
      expect(saved.config.headers_pattern).toEqual(config.headers_pattern);
      expect(saved.config.method).toBe("POST");
      expect(saved.config.followRedirects).toBe(false);
      expect(effectiveExpectedStatus(saved.config)).toEqual(["200"]);
      // The period observation from the same report: the check went from 5min
      // to 1min on that save. If that ever reproduces, it fails here.
      expect(saved.period).toBe("00:05:00");
    } finally {
      await deleteCheck(page, token, uid);
    }
  });

  test("a non-default snake expected_status survives the same save", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const uid = await createViaApi(page, token, "snake-status", {
      url: "https://acme.com/created",
      method: "POST",
      body: "{}",
      expected_status: 201,
    });

    try {
      await page.goto(`orgs/test/checks/${uid}/edit`);
      await page.waitForLoadState("networkidle");
      // The chip is seeded from the snake spelling; before the fix it showed
      // the default 200 and the save silently widened the check to accept it.
      await expect(
        page.getByTestId("check-expected-status-codes").getByText("201"),
      ).toBeVisible();

      await saveAndWait(page, uid);

      const saved = await getCheck(page, token, uid);
      expect(effectiveExpectedStatus(saved.config)).toEqual(["201"]);
      expect(saved.config.body).toBe("{}");
    } finally {
      await deleteCheck(page, token, uid);
    }
  });

  test("the body and headers are editable through the UI", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const uid = await createViaApi(page, token, "edit", {
      url: "https://acme.com/graphql",
      method: "POST",
      body: "{\"query\":\"{ ping }\"}",
      headers: { "content-type": "application/json" },
    });

    try {
      await page.goto(`orgs/test/checks/${uid}/edit`);
      await page.waitForLoadState("networkidle");
      // Advanced auto-opens: a body and a header both count as customized
      // (httpOptionsSummary), so the fields are visible without a click.
      await expect(page.getByTestId("check-http-body-input")).toHaveValue(
        "{\"query\":\"{ ping }\"}",
      );
      await expect(page.getByTestId("request-header-key-0")).toHaveValue(
        "content-type",
      );
      await expect(page.getByTestId("request-header-value-0")).toHaveValue(
        "application/json",
      );

      await page
        .getByTestId("check-http-body-input")
        .fill("{\"query\":\"{ pong }\"}");
      await page.getByTestId("request-header-add").click();
      await page.getByTestId("request-header-key-1").fill("accept");
      await page.getByTestId("request-header-value-1").fill("application/json");

      await saveAndWait(page, uid);

      const saved = await getCheck(page, token, uid);
      expect(saved.config.body).toBe("{\"query\":\"{ pong }\"}");
      expect(saved.config.headers).toEqual({
        "content-type": "application/json",
        accept: "application/json",
      });

      // Reload the edit page: both are shown as stored.
      await page.goto(`orgs/test/checks/${uid}/edit`);
      await page.waitForLoadState("networkidle");
      await expect(page.getByTestId("check-http-body-input")).toHaveValue(
        "{\"query\":\"{ pong }\"}",
      );
      // Row ORDER follows the stored map's key order, which the server is free
      // to re-emit either way — assert on the set, not on an index.
      const keys = await page
        .locator('[data-testid^="request-header-key-"]')
        .evaluateAll((els) =>
          els.map((el) => (el as HTMLInputElement).value).sort(),
        );
      expect(keys).toEqual(["accept", "content-type"]);
    } finally {
      await deleteCheck(page, token, uid);
    }
  });

  test("clearing the headers editor removes the key", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const uid = await createViaApi(page, token, "clear-headers", {
      url: "https://acme.com/clear-headers",
      method: "POST",
      body: "a=1",
      headers: { "x-trace": "1" },
      body_expect: "ok",
    });

    try {
      await page.goto(`orgs/test/checks/${uid}/edit`);
      await page.waitForLoadState("networkidle");
      await expect(page.getByTestId("request-header-key-0")).toHaveValue(
        "x-trace",
      );

      await page.getByTestId("request-header-remove-0").click();
      await expect(page.getByTestId("request-header-key-0")).toHaveCount(0);

      await saveAndWait(page, uid);

      const saved = await getCheck(page, token, uid);
      expect(saved.config).not.toHaveProperty("headers");
      // Omit-to-clear is scoped to the key the user cleared: the unmodeled
      // assertion and the body are untouched.
      expect(saved.config.body_expect).toBe("ok");
      expect(saved.config.body).toBe("a=1");
    } finally {
      await deleteCheck(page, token, uid);
    }
  });

  test("switching the method to GET and back keeps the body", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const uid = await createViaApi(page, token, "method-switch", {
      url: "https://acme.com/method-switch",
      method: "POST",
      body: "keep=me",
    });

    try {
      await page.goto(`orgs/test/checks/${uid}/edit`);
      await page.waitForLoadState("networkidle");
      await expect(page.getByTestId("check-http-body-input")).toHaveValue(
        "keep=me",
      );

      // GET hides the editor but must not destroy the value.
      await page.getByTestId("check-method-select").click();
      await page.getByRole("option", { name: "GET", exact: true }).click();
      await expect(page.getByTestId("check-http-body-input")).toHaveCount(0);

      await saveAndWait(page, uid);
      expect((await getCheck(page, token, uid)).config.body).toBe("keep=me");

      await page.goto(`orgs/test/checks/${uid}/edit`);
      await page.waitForLoadState("networkidle");
      await page.getByTestId("check-method-select").click();
      await page.getByRole("option", { name: "POST", exact: true }).click();
      await expect(page.getByTestId("check-http-body-input")).toHaveValue(
        "keep=me",
      );

      await saveAndWait(page, uid);
      const saved = await getCheck(page, token, uid);
      expect(saved.config.body).toBe("keep=me");
      expect(saved.config.method).toBe("POST");
    } finally {
      await deleteCheck(page, token, uid);
    }
  });
});
