import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-08-08-02: the OAuth 2.0 Device Authorization Grant
// (RFC 8628) behind `sp auth login`. The CLI half is exercised in Go; here we
// drive the browser half — a device request opened over the public API, then
// approved (or denied) on /dash0/device — and prove that approving really
// mints a PAT that shows up on the tokens page.
//
// The device endpoints speak the RFC's snake_case field names on purpose, so
// the API assertions below use device_code / user_code verbatim.

type DeviceAuthorization = {
  device_code: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete: string;
  expires_in: number;
  interval: number;
};

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

/** Opens a device authorization request through the public endpoint. */
async function startDeviceAuthorization(
  page: Page,
  clientName: string,
): Promise<DeviceAuthorization> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/device`, {
    data: { clientName },
  });
  expect(resp.status()).toBe(200);
  return (await resp.json()) as DeviceAuthorization;
}

/** Polls the token endpoint once and returns the parsed body. */
async function pollDeviceToken(page: Page, deviceCode: string) {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/auth/device/token`,
    {
      data: {
        grant_type: "urn:ietf:params:oauth:grant-type:device_code",
        device_code: deviceCode,
      },
      failOnStatusCode: false,
    },
  );
  return { status: resp.status(), body: await resp.json() };
}

/**
 * Polls until the server stops saying slow_down. The endpoint enforces the
 * advertised interval (5s), so a client that has already polled once — as the
 * "pending" assertion below does — must back off before the next attempt. This
 * mirrors what the real CLI does.
 */
async function collectDeviceToken(page: Page, deviceCode: string) {
  for (let attempt = 0; attempt < 4; attempt += 1) {
    const result = await pollDeviceToken(page, deviceCode);
    if (result.body.error !== "slow_down") return result;
    await page.waitForTimeout(5500);
  }
  return await pollDeviceToken(page, deviceCode);
}

test.describe("Device authorization consent", () => {
  test("approving a device request mints a PAT visible in the tokens list", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const clientName = `sp CLI e2e ${Date.now()}`;

    const authz = await startDeviceAuthorization(page, clientName);
    expect(authz.device_code).not.toBe("");
    expect(authz.user_code).toMatch(/^[A-Z2-9]{4}-[A-Z2-9]{4}$/);
    expect(authz.verification_uri).toContain("/dash0/device");

    // Before approval the client is told to keep waiting.
    const pending = await pollDeviceToken(page, authz.device_code);
    expect(pending.status).toBe(400);
    expect(pending.body.error).toBe("authorization_pending");

    // The complete verification URL pre-fills the code, exactly as the CLI's
    // best-effort browser open would.
    await page.goto(
      `orgs/test/account/device?user_code=${encodeURIComponent(authz.user_code)}`,
    );
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("device-user-code")).toHaveValue(
      authz.user_code,
    );
    await expect(page.getByTestId("device-consent-card")).toBeVisible();
    await expect(page.getByTestId("device-client-name")).toHaveText(clientName);

    await page.getByTestId("device-approve").click();
    await expect(page.getByTestId("device-approved")).toBeVisible();

    // The CLI's next poll gets the PAT — exactly once.
    const granted = await collectDeviceToken(page, authz.device_code);
    expect(granted.status).toBe(200);
    expect(granted.body.token_type).toBe("Bearer");
    expect(String(granted.body.access_token)).toContain("pat_");

    const replay = await pollDeviceToken(page, authz.device_code);
    expect(replay.status).toBe(400);
    expect(replay.body.error).toBe("expired_token");

    // The minted token is a real, named PAT on the tokens page.
    await page.goto("orgs/test/account/tokens");
    await page.waitForLoadState("networkidle");
    await expect(page.getByText(clientName).first()).toBeVisible();

    // Clean up so repeated runs do not pile tokens up.
    const token = await getAuthToken(page);
    const list = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/tokens?type=pat`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    const minted = ((await list.json()).data ?? []).find(
      (entry: { name?: string; uid: string }) => entry.name === clientName,
    );
    expect(minted, "the approved PAT must be listed").toBeTruthy();
    await page.request.delete(
      `${API_BASE}/api/v1/auth/tokens/${minted.uid}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
  });

  test("denying a device request grants nothing", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const clientName = `sp CLI denied ${Date.now()}`;

    const authz = await startDeviceAuthorization(page, clientName);

    await page.goto(
      `orgs/test/account/device?user_code=${encodeURIComponent(authz.user_code)}`,
    );
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("device-consent-card")).toBeVisible();
    await page.getByTestId("device-deny").click();
    await expect(page.getByTestId("device-denied")).toBeVisible();

    const denied = await pollDeviceToken(page, authz.device_code);
    expect(denied.status).toBe(400);
    expect(denied.body.error).toBe("access_denied");

    // No PAT was created for the denied request.
    const token = await getAuthToken(page);
    const list = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/tokens?type=pat`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    const names = ((await list.json()).data ?? []).map(
      (entry: { name?: string }) => entry.name,
    );
    expect(names).not.toContain(clientName);
  });

  test("an unknown code is reported, not silently swallowed", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/account/device?user_code=ZZZZ-9999");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("device-not-found")).toBeVisible();
    await expect(page.getByTestId("device-consent-card")).toHaveCount(0);
  });

  // Regression guard for the flow the whole feature exists for: approving on a
  // phone that is not signed in yet. The org-less /device route must bounce
  // through /login and come back with the one-time code still pre-filled — for
  // a real org slug ("test"), not just whatever slug the logged-out visitor was
  // routed through. Deliberately uses the plain (unauthenticated) `page`
  // fixture rather than `authenticatedPage`.
  test("a logged-out visitor keeps the pre-filled code across login", async ({
    page,
  }) => {
    const authz = await startDeviceAuthorization(
      page,
      `sp CLI loggedout ${Date.now()}`,
    );

    await page.goto(`device?user_code=${encodeURIComponent(authz.user_code)}`);

    // Bounced to the login form (org-less /login forwards to an org login).
    const loginTitle = page.getByTestId("login-title");
    await loginTitle.waitFor({ state: "visible", timeout: 15000 });
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();

    // This account belongs to several orgs, so the form may follow up with the
    // org picker; picking one still routes through resolveDestination, which
    // is the code path under test.
    const picker = page.getByTestId("org-picker-test");
    await Promise.race([
      picker.waitFor({ state: "visible", timeout: 10000 }).catch(() => null),
      page
        .waitForURL(/\/orgs\/test\/account\/device/, { timeout: 10000 })
        .catch(() => null),
    ]);
    if (await picker.isVisible().catch(() => false)) {
      await picker.click();
    }

    // Back on the consent page, under the session's REAL org ("test"), with
    // the code intact.
    await page.waitForURL(/\/orgs\/test\/account\/device/, { timeout: 20000 });
    await expect(page.getByTestId("device-user-code")).toHaveValue(
      authz.user_code,
    );
    await expect(page.getByTestId("device-consent-card")).toBeVisible();
  });

  test("the short /device URL forwards into the org consent page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const authz = await startDeviceAuthorization(page, `sp CLI short ${Date.now()}`);

    await page.goto(
      `device?user_code=${encodeURIComponent(authz.user_code)}`,
    );
    await page.waitForURL(/\/orgs\/[^/]+\/account\/device/);
    await expect(page.getByTestId("device-user-code")).toHaveValue(
      authz.user_code,
    );
  });
});
