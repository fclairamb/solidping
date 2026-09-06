import { test, expect } from "@playwright/test";
import { API_BASE } from "./fixtures";

/**
 * The shared public live demo (spec 2026-09-06-02).
 *
 * These run against a test-mode server, which enables the demo unconditionally
 * (SP_RUN_MODE=test forces `demo.enabled`, see config.Load) so the whole flow —
 * the login button, the persistent banner, a real check create, and the
 * read-only refusals — is exercisable without a second server configuration.
 *
 * Deliberately NOT using the `authenticatedPage` fixture: every test here is
 * about the demo session specifically, so each drives its own login.
 */

/** Reads the instance's public config, which is what the login button keys on. */
async function demoConfig(request: import("@playwright/test").APIRequestContext) {
  const response = await request.get(`${API_BASE}/api/v1/config`);
  expect(response.ok()).toBeTruthy();

  const body = await response.json();

  return body.demo as
    | { enabled: boolean; orgSlug?: string; email?: string; password?: string }
    | undefined;
}

test.describe("Public live demo", () => {
  test("the instance advertises a demo in test mode", async ({ request }) => {
    const demo = await demoConfig(request);

    expect(demo?.enabled).toBe(true);
    expect(demo?.orgSlug).toBeTruthy();
    expect(demo?.email).toBeTruthy();
    // The password is public BY DESIGN — the whole feature is "anyone can log
    // in and look around" — so it is served rather than hardcoded in the
    // bundle. See DemoPublicConfig.
    expect(demo?.password).toBeTruthy();
  });

  test("the login page offers a one-click entry into the demo", async ({ page }) => {
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    const demoButton = page.getByTestId("login-demo");
    await expect(demoButton).toBeVisible();

    await demoButton.click();

    // One click must land inside the demo org, banner and all.
    await page.waitForURL(/\/orgs\/[^/]+/, { timeout: 20000 });
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });
  });

  test("?demo=1 signs the visitor in on load", async ({ page }) => {
    // The marketing site's deep link: land in a working dashboard, not a form.
    await page.goto("orgs/test/login?demo=1");

    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });
  });

  test("the banner is on every page and cannot be dismissed", async ({ page }) => {
    await page.goto("orgs/test/login?demo=1");
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });

    const url = new URL(page.url());
    const org = url.pathname.split("/orgs/")[1]?.split("/")[0];
    expect(org).toBeTruthy();

    for (const path of ["checks", "incidents", "status-pages"]) {
      await page.goto(`orgs/${org}/${path}`);
      await page.waitForLoadState("networkidle");
      await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });
    }

    // No dismiss affordance anywhere on the banner.
    const banner = page.getByTestId("demo-banner");
    await expect(banner.getByRole("button", { name: /dismiss|close/i })).toHaveCount(0);
  });

  test("the settings pages offer a read-only note instead of a New button", async ({
    page,
  }) => {
    await page.goto("orgs/test/login?demo=1");
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });

    const url = new URL(page.url());
    const org = url.pathname.split("/orgs/")[1]?.split("/")[0];

    await page.goto(`orgs/${org}/integrations`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("integrations-demo-note")).toBeVisible({
      timeout: 20000,
    });

    await page.goto(`orgs/${org}/status-pages`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("status-pages-demo-note")).toBeVisible({
      timeout: 20000,
    });
  });

  test("a demo session can create a check and is told it will expire", async ({
    page,
    request,
  }) => {
    const demo = await demoConfig(request);
    const org = demo?.orgSlug as string;
    expect(org).toBeTruthy();

    await page.goto("orgs/test/login?demo=1");

    // Entering the demo from ANOTHER org's login page must land in the DEMO
    // org, not in the org whose login page this happened to be. Waited for
    // explicitly rather than read off page.url() once the banner shows: the
    // app settles on the demo org but transiently passes back through the
    // originating org on the way, so a single read can catch the intermediate
    // URL and then drive the rest of the test against an org this session is
    // not a member of. Asserting the destination is also the point — it is the
    // regression this test exists to catch.
    await page.waitForURL(new RegExp(`/orgs/${org}(/|$)`), { timeout: 20000 });
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });

    await page.goto(`orgs/${org}/checks/new`);
    await page.waitForLoadState("networkidle");

    // Creating a check is the ONE thing the demo exists to let a visitor do,
    // so this is the test that would catch a guard that closed too far.
    const slug = `e2e-demo-${Date.now()}`;

    const nameField = page.getByTestId("check-name-input");
    await nameField.waitFor({ state: "visible", timeout: 20000 });
    await nameField.fill(slug);

    const urlField = page.getByTestId("check-url-input");
    await urlField.waitFor({ state: "visible", timeout: 20000 });
    await urlField.fill(`${API_BASE}/api/v1/fake?period=86400`);

    await page.getByTestId("check-submit-button").click();

    // Landing on the detail page proves the create succeeded; the note is the
    // conversion hook.
    await page.waitForURL(/\/checks\/[0-9a-f-]{36}/, { timeout: 30000 });
    await expect(page.getByTestId("demo-check-note")).toBeVisible({ timeout: 20000 });
  });

  test("a write outside the allowlist is refused with DEMO_READ_ONLY", async ({
    page,
    request,
  }) => {
    // Straight at the API, because that is where the guarantee lives: the UI
    // merely declines to offer these buttons.
    const demo = await demoConfig(request);
    expect(demo?.enabled).toBe(true);

    const login = await request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: demo?.orgSlug, email: demo?.email, password: demo?.password },
    });
    expect(login.ok()).toBeTruthy();

    const { accessToken } = await login.json();
    expect(accessToken).toBeTruthy();

    const refused = await request.post(
      `${API_BASE}/api/v1/orgs/${demo?.orgSlug}/status-pages`,
      {
        headers: { Authorization: `Bearer ${accessToken}` },
        data: { name: "Nope", slug: "nope" },
      },
    );

    expect(refused.status()).toBe(403);
    expect((await refused.json()).code).toBe("DEMO_READ_ONLY");

    // Positive control: reading is fine, so the credential itself is good.
    const read = await request.get(`${API_BASE}/api/v1/orgs/${demo?.orgSlug}/checks`, {
      headers: { Authorization: `Bearer ${accessToken}` },
    });
    expect(read.ok()).toBeTruthy();

    await page.close();
  });

  test("a seeded catalogue check cannot be deleted", async ({ page, request }) => {
    const demo = await demoConfig(request);

    const login = await request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: demo?.orgSlug, email: demo?.email, password: demo?.password },
    });
    const { accessToken } = await login.json();

    const list = await request.get(`${API_BASE}/api/v1/orgs/${demo?.orgSlug}/checks`, {
      headers: { Authorization: `Bearer ${accessToken}` },
    });
    const { data } = await list.json();

    const seeded = (data as { uid: string; createdBy?: string | null }[]).find(
      (check) => !check.createdBy,
    );
    expect(seeded, "the demo org should carry a seeded, un-owned catalogue").toBeTruthy();

    const refused = await request.delete(
      `${API_BASE}/api/v1/orgs/${demo?.orgSlug}/checks/${seeded?.uid}`,
      { headers: { Authorization: `Bearer ${accessToken}` } },
    );

    expect(refused.status()).toBe(403);
    expect((await refused.json()).code).toBe("DEMO_READ_ONLY");

    await page.close();
  });

  test("the demo account's password cannot be reset", async ({ request }) => {
    // The unauthenticated path the write guard cannot see. A bare request
    // without a valid token is refused anyway; what matters here is that
    // requesting a reset for the demo address does not silently rotate the
    // shared credential — the login below is the proof.
    const demo = await demoConfig(request);

    await request.post(`${API_BASE}/api/v1/auth/request-password-reset`, {
      data: { org: demo?.orgSlug, email: demo?.email },
    });

    const login = await request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: demo?.orgSlug, email: demo?.email, password: demo?.password },
    });

    expect(login.ok()).toBeTruthy();
  });
});
