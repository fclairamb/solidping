import {
  test,
  expect,
  API_BASE,
  escapeRegExp,
  stubSavedPreferences,
  uniqueStamp,
  type Page,
  type SavedPreferences,
} from "./fixtures";

// The dashboard keeps the language and timezone the server writes emails in
// matched with the browser (spec 2026-10-21-01, decision 5). Every test that
// signs in as test@test.com answers PATCH /auth/me locally
// (stubSavedPreferences): that account is shared by every worker, and a
// language saved on it would switch them all. The sessions that must never
// write (impersonation, demo, forced password change) run for real, on
// accounts of their own.

const TIMEZONE = "Asia/Tokyo";
const PASSWORD = "Strong-Pass-123!";

/** Every PATCH /auth/me body this page sends, in order. */
function recordPreferenceWrites(page: Page): SavedPreferences[] {
  const writes: SavedPreferences[] = [];
  page.on("request", (request) => {
    if (
      request.method() === "PATCH" &&
      new URL(request.url()).pathname.endsWith("/api/v1/auth/me")
    ) {
      const body = request.postDataJSON() as { preferences?: SavedPreferences };
      writes.push(body.preferences ?? {});
    }
  });
  return writes;
}

/** The preferences in the last /auth/me answer this page read. */
function recordServerPreferences(page: Page): { last?: SavedPreferences } {
  const seen: { last?: SavedPreferences } = {};
  page.on("response", async (response) => {
    const request = response.request();
    if (
      request.method() !== "GET" ||
      !new URL(response.url()).pathname.endsWith("/api/v1/auth/me") ||
      !response.ok()
    ) {
      return;
    }
    const body = (await response.json().catch(() => null)) as {
      user?: { preferences?: SavedPreferences };
    } | null;
    if (body?.user) {
      seen.last = body.user.preferences;
    }
  });
  return seen;
}

/** A fresh account with nothing saved, or the test is skipped. */
async function seedUser(
  page: Page,
  email: string,
  extra: Record<string, unknown> = {},
): Promise<void> {
  const created = await page.request.post(`${API_BASE}/api/v1/test/users`, {
    data: { email, password: PASSWORD, name: "Email Language", ...extra },
  });
  if (created.status() !== 201) {
    test.skip(
      true,
      `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${created.status()}`,
    );
  }
}

test.describe("Email language", () => {
  test.use({ timezoneId: TIMEZONE });

  test("a first sync saves the browser's language and timezone, once", async ({
    authenticatedPage: page,
  }) => {
    let saved: SavedPreferences = { locale: "", timezone: "" };
    const patches: SavedPreferences[] = [];
    await stubSavedPreferences(
      page,
      () => saved,
      (body) => {
        patches.push(body.preferences ?? {});
        saved = { ...saved, ...body.preferences };
      },
    );

    await page.goto("orgs/test/account/profile");

    // Apart, the language first: a refused timezone cannot take it down.
    await expect.poll(() => patches).toEqual([{ locale: "en" }, { timezone: TIMEZONE }]);
    await expect(page.getByTestId("profile-email-timezone")).toContainText(TIMEZONE);

    // Saved now: a new page load writes nothing.
    await page.reload();
    await expect(page.getByTestId("profile-email-language-card")).toBeVisible();
    await page.waitForLoadState("networkidle");
    expect(patches).toHaveLength(2);
  });

  test("a refused timezone does not stop the language from being saved", async ({
    authenticatedPage: page,
  }) => {
    let saved: SavedPreferences = { locale: "", timezone: "" };
    const patches: SavedPreferences[] = [];
    await stubSavedPreferences(
      page,
      () => saved,
      (body) => {
        const prefs = body.preferences ?? {};
        patches.push(prefs);
        // The server refuses a zone its tzdata lacks (or ICU's Etc/Unknown).
        if (prefs.timezone) {
          return false;
        }
        saved = { ...saved, ...prefs };
      },
    );

    await page.goto("orgs/test/account/profile");

    await expect.poll(() => patches).toEqual([{ locale: "en" }, { timezone: TIMEZONE }]);
    expect(saved).toEqual({ locale: "en", timezone: "" });
    // Refused once per page load, never retried in a loop.
    await expect(page.getByTestId("profile-email-language-card")).toBeVisible();
    await page.waitForLoadState("networkidle");
    expect(patches).toHaveLength(2);
  });

  test("a saved language wins over the browser's", async ({
    authenticatedPage: page,
  }) => {
    const patches: SavedPreferences[] = [];
    await stubSavedPreferences(
      page,
      () => ({ locale: "fr", timezone: TIMEZONE }),
      (body) => {
        patches.push(body.preferences ?? {});
      },
    );

    await page.goto("orgs/test/account/profile");

    const card = page.getByTestId("profile-email-language-card");
    await expect(card).toContainText("Langue des e-mails");
    await expect(page.getByTestId("profile-email-language-select")).toContainText(
      "Français",
    );
    await page.waitForLoadState("networkidle");
    expect(patches).toEqual([]);
  });

  test("the profile card saves the email language, and the dashboard follows", async ({
    authenticatedPage: page,
  }) => {
    let saved: SavedPreferences = { locale: "en", timezone: TIMEZONE };
    const patches: SavedPreferences[] = [];
    await stubSavedPreferences(
      page,
      () => saved,
      (body) => {
        patches.push(body.preferences ?? {});
        saved = { ...saved, ...body.preferences };
      },
    );

    await page.goto("orgs/test/account/profile");
    const card = page.getByTestId("profile-email-language-card");
    await expect(card).toContainText("Email language");

    await page.getByTestId("profile-email-language-select").click();
    await page.getByTestId("profile-email-language-option-de").click();

    await expect.poll(() => patches).toEqual([{ locale: "de" }]);
    await expect(page.getByTestId("profile-email-language-saved")).toHaveText(
      "E-Mail-Sprache gespeichert.",
    );
    await expect(card).toContainText("E-Mail-Sprache");
    await expect(page.getByTestId("profile-email-language-select")).toContainText(
      "Deutsch",
    );
  });

  test("the sidebar language menu saves the choice when signed in", async ({
    authenticatedPage: page,
  }) => {
    let saved: SavedPreferences = { locale: "en", timezone: TIMEZONE };
    const patches: SavedPreferences[] = [];
    await stubSavedPreferences(
      page,
      () => saved,
      (body) => {
        patches.push(body.preferences ?? {});
        saved = { ...saved, ...body.preferences };
      },
    );

    await page.goto("orgs/test/account/profile");
    await expect(page.getByTestId("profile-email-language-card")).toContainText(
      "Email language",
    );

    await page.getByTestId("language-switcher").click();
    await page.getByTestId("language-switcher-option-es").click();

    await expect.poll(() => patches).toEqual([{ locale: "es" }]);
    await expect(page.getByTestId("profile-email-language-card")).toContainText(
      "Idioma de los correos",
    );
  });

  test("signed out, the language menu stays in this browser", async ({ page }) => {
    const writes: string[] = [];
    page.on("request", (request) => {
      if (request.method() === "PATCH" && request.url().includes("/api/v1/auth/me")) {
        writes.push(request.url());
      }
    });

    await page.goto("orgs/test/login");
    await expect(page.getByTestId("login-submit")).toHaveText("Sign in");

    await page.getByTestId("language-switcher").click();
    await page.getByTestId("language-switcher-option-fr").click();

    await expect(page.getByTestId("login-submit")).toHaveText("Se connecter");
    await page.waitForLoadState("networkidle");
    expect(writes).toEqual([]);
  });

  test("register sends the page's language and the browser's timezone", async ({
    page,
  }) => {
    await page.addInitScript(() => {
      localStorage.setItem("solidping_language", "fr");
    });
    // Answered here: no pending registration is created.
    let sent: Record<string, unknown> | null = null;
    await page.route("**/api/v1/auth/register", async (route) => {
      sent = route.request().postDataJSON() as Record<string, unknown>;
      await route.fulfill({ status: 200, json: { message: "ok" } });
    });

    await page.goto("orgs/test/register");
    await page.locator("#email").fill("alice@acme.com");
    await page.locator("#password").fill("correct-horse-battery");
    await page.locator('button[type="submit"]').click();

    await expect.poll(() => sent).toMatchObject({
      email: "alice@acme.com",
      locale: "fr",
      timezone: TIMEZONE,
    });
  });

  test("accepting an invitation sends the page's language and the browser's timezone", async ({
    page,
  }) => {
    await page.addInitScript(() => {
      localStorage.setItem("solidping_language", "es");
    });
    const token = "e2e-email-language-invite";
    await page.route(`**/api/v1/auth/invite/${token}`, (route) =>
      route.fulfill({
        status: 200,
        json: { orgName: "Acme", orgSlug: "acme", email: "alice@acme.com", role: "user" },
      }),
    );
    // Refused here, so no session is created and the page stays put.
    let sent: Record<string, unknown> | null = null;
    await page.route("**/api/v1/auth/accept-invite", async (route) => {
      sent = route.request().postDataJSON() as Record<string, unknown>;
      await route.fulfill({
        status: 400,
        json: { title: "stubbed", code: "VALIDATION_ERROR" },
      });
    });

    await page.goto(`invite/${token}`);
    await page.locator("#password").fill("correct-horse-battery");
    await page.locator('button[type="submit"]').click();

    await expect.poll(() => sent).toMatchObject({
      token,
      locale: "es",
      timezone: TIMEZONE,
    });
  });

  test("viewing the dashboard as another user writes nothing to their account", async ({
    authenticatedPage: page,
    browser,
  }) => {
    const email = `email-language-target-${uniqueStamp()}@example.test`;
    await seedUser(page, email);
    const added = await page.request.post(`${API_BASE}/api/v1/orgs/test/members`, {
      data: { email, role: "user" },
    });
    expect(added.status()).toBe(201);

    await page.goto("orgs/test/server/users");
    await page.waitForLoadState("networkidle");
    const searched = page.waitForResponse(
      (resp) =>
        resp.url().includes("/api/v1/system/users?") &&
        new URL(resp.url()).searchParams.get("q") === email,
    );
    await page.getByTestId("users-search").fill(email);
    await searched;
    const row = page.getByRole("row", { name: new RegExp(escapeRegExp(email)) });
    await row.getByRole("button", { name: "View as this user" }).click();

    const writes = recordPreferenceWrites(page);
    await page.getByTestId("impersonate-confirm").click();
    await expect(page.getByTestId("impersonation-banner")).toContainText(email);

    await page.goto("orgs/test/account/profile");
    await expect(page.getByTestId("impersonation-banner")).toBeVisible();
    // The member's own saved values (none: English, UTC), read-only, not
    // tied to the admin's dashboard or browser.
    await expect(page.getByTestId("profile-email-language-subtitle")).toHaveText(
      "The language and timezone of this account's emails. They cannot be changed while viewing the dashboard as another user.",
    );
    await expect(page.getByTestId("profile-email-timezone")).toHaveText("Timezone: UTC.");
    await expect(page.getByTestId("profile-email-language-select")).toBeDisabled();
    await page.waitForLoadState("networkidle");
    expect(writes).toEqual([]);

    // Positive control: the same account, signed in for real, does write.
    const context = await browser.newContext({ timezoneId: TIMEZONE });
    const member = await context.newPage();
    const memberWrites = recordPreferenceWrites(member);
    await member.goto("orgs/test/login");
    await member.getByTestId("login-email").fill(email);
    await member.getByTestId("login-password").fill(PASSWORD);
    await member.getByTestId("login-submit").click();
    // Contains, not equals: a write cut short by the post-login navigation
    // is sent again on the next page load.
    await expect
      .poll(() => memberWrites)
      .toEqual(expect.arrayContaining([{ locale: "en" }, { timezone: TIMEZONE }]));
    await context.close();
  });

  test("a session confined to the password rotation writes nothing", async ({ page }) => {
    const oldPassword = "Seeded-Pass-123!";
    const email = `email-language-rotation-${uniqueStamp()}@example.test`;
    await seedUser(page, email, { password: oldPassword, mustChangePassword: true });

    const login = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email, password: oldPassword },
    });
    expect(login.status()).toBe(200);
    const session = (await login.json()) as {
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
      user: { mustChangePassword?: boolean };
    };
    expect(session.user.mustChangePassword).toBe(true);
    await page.addInitScript(
      ({ accessToken, refreshToken, expiresIn }) => {
        localStorage.setItem("solidping_session_token", accessToken);
        if (refreshToken) {
          localStorage.setItem("solidping_refresh_token", refreshToken);
        }
        if (expiresIn) {
          localStorage.setItem("solidping_expires_at", String(Date.now() + expiresIn * 1000));
          localStorage.setItem("solidping_expires_in", String(expiresIn));
        }
      },
      {
        accessToken: session.accessToken,
        refreshToken: session.refreshToken ?? "",
        expiresIn: session.expiresIn ?? 0,
      },
    );

    const writes = recordPreferenceWrites(page);
    const server = recordServerPreferences(page);
    await page.goto("change-password");
    await expect(page.getByTestId("forced-password-change")).toBeVisible();
    await page.waitForLoadState("networkidle");
    await page.reload();
    await expect(page.getByTestId("forced-password-change")).toBeVisible();
    await page.waitForLoadState("networkidle");
    // Nothing saved, so an unconfined session would have written both.
    expect(server.last?.locale ?? "").toBe("");
    expect(writes).toEqual([]);

    // Positive control: once the rotation is done, the same session writes.
    const newPassword = "Rotated-Pass-456!";
    await page.getByTestId("forced-password-current").fill(oldPassword);
    await page.getByTestId("forced-password-new").fill(newPassword);
    await page.getByTestId("forced-password-confirm").fill(newPassword);
    await page.getByTestId("forced-password-submit").click();
    await expect
      .poll(() => writes)
      .toEqual(expect.arrayContaining([{ locale: "en" }, { timezone: TIMEZONE }]));
  });

  test("the shared demo writes nothing to the demo account", async ({ page, request }) => {
    const config = await request.get(`${API_BASE}/api/v1/config`);
    expect(config.ok()).toBeTruthy();
    const demo = ((await config.json()) as { demo?: { enabled: boolean; orgSlug?: string } })
      .demo;
    test.skip(!demo?.enabled || !demo.orgSlug, "the live demo is not enabled");
    const org = demo?.orgSlug as string;

    const writes = recordPreferenceWrites(page);
    const server = recordServerPreferences(page);
    await page.goto("orgs/test/login?demo=1");
    await page.waitForURL(new RegExp(`/orgs/${org}(/|$)`), { timeout: 20000 });
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });

    await page.goto(`orgs/${org}/account/profile`);
    await expect(page.getByTestId("profile-email-language-subtitle")).toHaveText(
      "The language and timezone of this account's emails. They cannot be changed in the demo.",
    );
    await expect(page.getByTestId("profile-email-language-select")).toBeDisabled();
    await page.waitForLoadState("networkidle");
    // The demo account's timezone is not this browser's, so a normal session
    // would have written it.
    expect(server.last).toBeDefined();
    expect(server.last?.timezone ?? "").not.toBe(TIMEZONE);
    expect(writes).toEqual([]);
  });
});
