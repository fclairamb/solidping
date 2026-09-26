import { test, expect } from "@playwright/test";

// Spec 2026-09-26-01: with the UI in French, the org login page's passkey
// button read "Sign in with passkey". fr/auth.json held the English string, so
// key parity passed. The unit guard (src/locales/untranslated-values.test.ts)
// now catches the file side; this checks what the user actually sees.
//
// The passkey control only renders when /auth/providers says passkeys are
// enabled, which depends on the backend's WebAuthn config. The response is
// patched so the test runs on every backend instead of skipping.

const EXPECTED: Record<string, string> = {
  // Positive control: the same button in English, so a broken selector or a
  // button that never renders cannot pass as "translated".
  en: "Sign in with passkey",
  fr: "Se connecter avec une clé d'accès",
  de: "Mit Passkey anmelden",
  es: "Iniciar sesión con llave de acceso",
};

test.describe("Login: passkey button is translated", () => {
  for (const [language, label] of Object.entries(EXPECTED)) {
    test(`shows "${label}" in ${language}`, async ({ page }) => {
      await page.addInitScript((lng) => {
        localStorage.setItem("solidping_language", lng);
      }, language);
      await page.route("**/api/v1/auth/providers*", async (route) => {
        const response = await route.fetch();
        const body = (await response.json()) as Record<string, unknown>;
        await route.fulfill({ response, json: { ...body, passkeysEnabled: true } });
      });

      await page.goto("orgs/test/login");

      const button = page.getByTestId("passkey-login-button");
      await expect(button).toBeVisible();
      await expect(button).toHaveText(label);
      if (language !== "en") {
        await expect(button).not.toHaveText(EXPECTED.en);
      }
    });
  }
});
