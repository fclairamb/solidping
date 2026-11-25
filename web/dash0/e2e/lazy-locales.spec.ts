import { test, expect } from "@playwright/test";

// Spec 2026-10-31-03: fr, de, es and zh are lazily loaded, one chunk per
// language. English stays in the main chunk. The org login page shows the
// passkey button (see login-passkey-i18n.spec.ts), a cheap translated string to
// assert on.

const LOCALE_CHUNK = /\/assets\/(fr|de|es|zh)-[\w-]+\.js/;

function recordLocaleChunks(page: import("@playwright/test").Page): string[] {
  const chunks: string[] = [];
  page.on("request", (request) => {
    const match = new URL(request.url()).pathname.match(LOCALE_CHUNK);
    if (match) chunks.push(match[1]);
  });
  return chunks;
}

async function enablePasskeys(page: import("@playwright/test").Page) {
  await page.route("**/api/v1/auth/providers*", async (route) => {
    const response = await route.fetch();
    const body = (await response.json()) as Record<string, unknown>;
    await route.fulfill({ response, json: { ...body, passkeysEnabled: true } });
  });
}

test.describe("Lazy locales", () => {
  test("a French visitor sees French on first paint and loads only the fr chunk", async ({
    page,
  }) => {
    await page.addInitScript(() => localStorage.setItem("solidping_language", "fr"));
    await enablePasskeys(page);
    const chunks = recordLocaleChunks(page);

    await page.goto("orgs/test/login");

    const button = page.getByTestId("passkey-login-button");
    await expect(button).toHaveText("Se connecter avec une clé d'accès");
    await expect(button).not.toHaveText("Sign in with passkey");
    expect(chunks).toContain("fr");
    expect(chunks).not.toContain("de");
    expect(chunks).not.toContain("es");
    expect(chunks).not.toContain("zh");
  });

  test("a Chinese visitor sees Simplified Chinese on first paint and loads only the zh chunk", async ({
    page,
  }) => {
    await page.addInitScript(() => localStorage.setItem("solidping_language", "zh"));
    await enablePasskeys(page);
    const chunks = recordLocaleChunks(page);

    await page.goto("orgs/test/login");

    const button = page.getByTestId("passkey-login-button");
    await expect(button).toHaveText("使用通行密钥登录");
    await expect(button).not.toHaveText("Sign in with passkey");
    expect(chunks).toContain("zh");
    expect(chunks).not.toContain("fr");
    expect(chunks).not.toContain("de");
    expect(chunks).not.toContain("es");
  });

  test("switching to de updates the UI without a reload and requests the de chunk", async ({
    page,
  }) => {
    await page.addInitScript(() => {
      if (!localStorage.getItem("solidping_language")) {
        localStorage.setItem("solidping_language", "en");
      }
    });
    await enablePasskeys(page);
    const chunks = recordLocaleChunks(page);

    await page.goto("orgs/test/login");
    const button = page.getByTestId("passkey-login-button");
    await expect(button).toHaveText("Sign in with passkey");
    expect(chunks).toEqual([]);

    await page.evaluate(() => {
      (window as unknown as { __noReload: boolean }).__noReload = true;
    });
    await page.getByTestId("language-switcher").click();
    await page.getByTestId("language-switcher-option-de").click();

    await expect(button).toHaveText("Mit Passkey anmelden");
    expect(chunks).toContain("de");
    expect(
      await page.evaluate(() => (window as unknown as { __noReload?: boolean }).__noReload),
    ).toBe(true);
  });

  test("an English visitor requests no fr, de, es or zh chunk", async ({ page }) => {
    await page.addInitScript(() => localStorage.setItem("solidping_language", "en"));
    await enablePasskeys(page);
    const chunks = recordLocaleChunks(page);

    await page.goto("orgs/test/login");
    await expect(page.getByTestId("passkey-login-button")).toHaveText("Sign in with passkey");
    expect(chunks).toEqual([]);
  });

  test("?lang= picks the language on any page, then leaves the URL", async ({ page }) => {
    await page.addInitScript(() => {
      if (!sessionStorage.getItem("lang-seeded")) {
        sessionStorage.setItem("lang-seeded", "1");
        localStorage.setItem("solidping_language", "en");
      }
    });
    await enablePasskeys(page);

    await page.goto("orgs/test/login?lang=es");

    const button = page.getByTestId("passkey-login-button");
    await expect(button).toHaveText("Iniciar sesión con llave de acceso");
    await expect(page).not.toHaveURL(/[?&]lang=/);
    expect(await page.evaluate(() => localStorage.getItem("solidping_language"))).toBe("es");

    // The pick sticks without the parameter.
    await page.reload();
    await expect(button).toHaveText("Iniciar sesión con llave de acceso");
  });

  test("an unknown ?lang= is dropped and keeps the current language", async ({ page }) => {
    await page.addInitScript(() => localStorage.setItem("solidping_language", "en"));
    await enablePasskeys(page);

    await page.goto("orgs/test/login?lang=xx");

    await expect(page.getByTestId("passkey-login-button")).toHaveText("Sign in with passkey");
    await expect(page).not.toHaveURL(/[?&]lang=/);
  });
});
