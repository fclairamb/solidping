import { test, expect, API_BASE } from "./fixtures";

// Spec 2026-08-23-04: a fresh database seeds admin@solidping.io / solidpass as
// a superadmin, and both halves of that pair are published in a public
// repository. The fix is not secrecy — the seed is unchanged — but a forced
// rotation: the first successful login yields a session that can do nothing
// except set a new password.
//
// These tests use a THROWAWAY user seeded with the flag, never the shared
// `test@test.com` fixture. That account is deliberately unflagged (asserted in
// server/test/integration/startup_forced_rotation_test.go): flagging it would
// confine every other spec in this suite to the rotation screen.
test.describe("Forced password change", () => {
  const OLD_PASSWORD = "Seeded-Pass-123!";

  async function seedFlaggedUser(page: import("@playwright/test").Page) {
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const email = `forced-rot-${stamp}@unknown.example`;

    const createResp = await page.request.post(`${API_BASE}/api/v1/test/users`, {
      data: {
        email,
        password: OLD_PASSWORD,
        name: "Forced Rotation User",
        mustChangePassword: true,
      },
    });
    if (createResp.status() !== 201) {
      test.skip(
        true,
        `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${createResp.status()}`,
      );
    }

    const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email, password: OLD_PASSWORD },
    });
    // The login itself SUCCEEDS — that is the design. What it hands back is a
    // session confined to the rotation, flagged by `mustChangePassword`.
    expect(loginResp.status()).toBe(200);
    const login = (await loginResp.json()) as {
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
      user: { mustChangePassword?: boolean };
    };
    expect(login.user.mustChangePassword).toBe(true);

    return { email, login };
  }

  async function seedBrowserSession(
    page: import("@playwright/test").Page,
    session: { accessToken: string; refreshToken?: string; expiresIn?: number },
  ) {
    await page.addInitScript(
      ({ accessToken, refreshToken, expiresIn }) => {
        localStorage.setItem("solidping_session_token", accessToken as string);
        if (refreshToken) {
          localStorage.setItem("solidping_refresh_token", refreshToken as string);
        }
        if (expiresIn) {
          localStorage.setItem(
            "solidping_expires_at",
            String(Date.now() + Number(expiresIn) * 1000),
          );
          localStorage.setItem("solidping_expires_in", String(expiresIn));
        }
      },
      {
        accessToken: session.accessToken,
        refreshToken: session.refreshToken ?? "",
        expiresIn: session.expiresIn ?? 0,
      },
    );
  }

  test("logging in as a flagged account lands on the rotation screen", async ({
    page,
  }) => {
    const { login } = await seedFlaggedUser(page);
    await seedBrowserSession(page, login);

    await page.goto("change-password");
    await expect(page.getByTestId("forced-password-change")).toBeVisible();
    await expect(page.getByTestId("forced-password-new")).toBeVisible();
  });

  test("navigating anywhere else bounces straight back", async ({ page }) => {
    const { login } = await seedFlaggedUser(page);
    await seedBrowserSession(page, login);

    // Any org route fires a data fetch, which answers 403
    // PASSWORD_CHANGE_REQUIRED and returns the browser to the rotation screen.
    // This is the property that makes the screen inescapable.
    await page.goto("orgs/test/checks");
    await page.waitForURL((url) => url.pathname.endsWith("/change-password"), {
      timeout: 15000,
    });
    await expect(page.getByTestId("forced-password-change")).toBeVisible();
  });

  test("completing the rotation unblocks the account", async ({ page }) => {
    const { email, login } = await seedFlaggedUser(page);
    await seedBrowserSession(page, login);

    const newPassword = "Rotated-Pass-456!";

    await page.goto("change-password");
    await expect(page.getByTestId("forced-password-change")).toBeVisible();

    await page.getByTestId("forced-password-current").fill(OLD_PASSWORD);
    await page.getByTestId("forced-password-new").fill(newPassword);
    await page.getByTestId("forced-password-confirm").fill(newPassword);
    await page.getByTestId("forced-password-submit").click();

    await page.waitForURL((url) => !url.pathname.endsWith("/change-password"), {
      timeout: 15000,
    });

    // The rotation cleared the flag server-side: a fresh login with the NEW
    // password is an ordinary, unconfined session.
    const relogin = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email, password: newPassword },
    });
    expect(relogin.status()).toBe(200);
    const reloginBody = (await relogin.json()) as {
      user: { mustChangePassword?: boolean };
    };
    expect(reloginBody.user.mustChangePassword).toBeFalsy();
  });

  test("a wrong current password keeps the user on the screen", async ({
    page,
  }) => {
    const { login } = await seedFlaggedUser(page);
    await seedBrowserSession(page, login);

    await page.goto("change-password");
    await expect(page.getByTestId("forced-password-change")).toBeVisible();

    await page.getByTestId("forced-password-current").fill("Definitely-Wrong-1!");
    await page.getByTestId("forced-password-new").fill("Rotated-Pass-456!");
    await page.getByTestId("forced-password-confirm").fill("Rotated-Pass-456!");
    await page.getByTestId("forced-password-submit").click();

    await expect(page.getByTestId("forced-password-error")).toBeVisible();
    await expect(page).toHaveURL(/change-password/);
  });

  test("renders on a phone-sized viewport", async ({ page }) => {
    const { login } = await seedFlaggedUser(page);
    await seedBrowserSession(page, login);

    await page.setViewportSize({ width: 375, height: 720 });
    await page.goto("change-password");

    await expect(page.getByTestId("forced-password-change")).toBeVisible();
    await expect(page.getByTestId("forced-password-submit")).toBeVisible();

    // No horizontal overflow on the narrowest supported width.
    const overflows = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth + 1,
    );
    expect(overflows).toBe(false);
  });
});
