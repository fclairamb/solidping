import {
  test,
  expect,
  stubSavedPreferences,
  type SavedPreferences,
} from "./fixtures";

// The dashboard keeps the language and timezone the server writes emails in
// matched with the browser (spec 2026-10-21-01, decision 5). Every test here
// answers PATCH /auth/me locally (stubSavedPreferences): test@test.com is
// shared by every worker, and a language saved on it would switch them all.

const TIMEZONE = "Asia/Tokyo";

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

    await expect.poll(() => patches).toEqual([{ locale: "en", timezone: TIMEZONE }]);
    await expect(page.getByTestId("profile-email-timezone")).toContainText(TIMEZONE);

    // Saved now: a new page load writes nothing.
    await page.reload();
    await expect(page.getByTestId("profile-email-language-card")).toBeVisible();
    await page.waitForLoadState("networkidle");
    expect(patches).toHaveLength(1);
  });

  test("a saved language wins over the browser's", async ({
    authenticatedPage: page,
  }) => {
    const patches: SavedPreferences[] = [];
    await stubSavedPreferences(
      page,
      () => ({ locale: "fr", timezone: TIMEZONE }),
      (body) => patches.push(body.preferences ?? {}),
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
});
