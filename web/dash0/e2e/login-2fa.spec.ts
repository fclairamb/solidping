import { test, expect, API_BASE } from "./fixtures";
import { generateTotp } from "./totp-utils";

// Login-time 2FA verification. This flow was broken from day one: the client
// sent the challenge's temp token in the JSON body ({ tempToken, code }),
// while the backend only ever reads it from the Authorization: Bearer
// header, so every TOTP login (and recovery-code login) died on
// "Temporary token required". The setup/confirm flow was covered by
// account-security-2fa.spec.ts, but nothing drove the login → challenge →
// verify path itself — these tests close that gap and fail on the pre-fix
// client.
test.describe("Login with 2FA", () => {
  const PASSWORD = "Login-2fa-123!";

  // Seed a throwaway user + org, then enroll TOTP entirely over the API so
  // the tests only drive the login UI. Throwaway (the account-password
  // pattern) because enabling 2FA on the shared test@test.com user would
  // lock every other spec out of its login.
  async function seedEnrolledUser(page: import("@playwright/test").Page) {
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
    const email = `login-2fa-${stamp}@unknown.example`;

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password: PASSWORD, name: "Login 2FA User" } },
    );
    if (createUserResp.status() !== 201) {
      test.skip(
        true,
        `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${createUserResp.status()}`,
      );
    }

    const loginResp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email, password: PASSWORD },
    });
    expect(loginResp.status()).toBe(200);
    const login = (await loginResp.json()) as { accessToken: string };

    const orgSlug = `l2f-${stamp.toString(36)}`;
    const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${login.accessToken}` },
      data: { name: `Login 2FA Co ${stamp}`, slug: orgSlug },
    });
    expect(createOrgResp.status()).toBe(201);
    const org = (await createOrgResp.json()) as { accessToken: string };

    const setupResp = await page.request.post(
      `${API_BASE}/api/v1/auth/2fa/setup`,
      { headers: { Authorization: `Bearer ${org.accessToken}` } },
    );
    expect(setupResp.status()).toBe(200);
    const setup = (await setupResp.json()) as { secret: string };

    const confirmResp = await page.request.post(
      `${API_BASE}/api/v1/auth/2fa/confirm`,
      {
        headers: { Authorization: `Bearer ${org.accessToken}` },
        data: { code: generateTotp(setup.secret) },
      },
    );
    expect(confirmResp.status()).toBe(200);
    const confirm = (await confirmResp.json()) as { recoveryCodes: string[] };

    return {
      email,
      orgSlug,
      secret: setup.secret,
      recoveryCodes: confirm.recoveryCodes,
    };
  }

  async function loginToChallenge(
    page: import("@playwright/test").Page,
    orgSlug: string,
    email: string,
  ) {
    await page.goto(`orgs/${orgSlug}/login`);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("login-email").fill(email);
    await page.getByTestId("login-password").fill(PASSWORD);
    await page.getByTestId("login-submit").click();
  }

  test("password + TOTP code signs in and lands on the org", async ({
    page,
  }) => {
    const { email, orgSlug, secret } = await seedEnrolledUser(page);

    await loginToChallenge(page, orgSlug, email);

    const codeInput = page.getByTestId("2fa-login-code");
    await expect(codeInput).toBeVisible();
    await codeInput.fill(generateTotp(secret));
    await page.getByTestId("2fa-login-verify").click();

    // Pre-fix, verification 401s with "Temporary token required" and the
    // page never leaves /login.
    await page.waitForURL((url) => !url.pathname.includes("/login"), {
      timeout: 10000,
    });
    await expect(page.getByTestId("login-error")).toHaveCount(0);
  });

  test("a recovery code signs in when the authenticator is lost", async ({
    page,
  }) => {
    const { email, orgSlug, recoveryCodes } = await seedEnrolledUser(page);
    expect(recoveryCodes.length).toBeGreaterThan(0);

    await loginToChallenge(page, orgSlug, email);

    await expect(page.getByTestId("2fa-login-code")).toBeVisible();
    await page.getByTestId("2fa-login-recovery-link").click();

    const recoveryInput = page.getByTestId("2fa-login-recovery-code");
    await expect(recoveryInput).toBeVisible();
    await recoveryInput.fill(recoveryCodes[0]);
    await page.getByTestId("2fa-login-verify").click();

    await page.waitForURL((url) => !url.pathname.includes("/login"), {
      timeout: 10000,
    });
    await expect(page.getByTestId("login-error")).toHaveCount(0);
  });

  test("a wrong TOTP code shows an error and stays on the challenge", async ({
    page,
  }) => {
    const { email, orgSlug, secret } = await seedEnrolledUser(page);

    await loginToChallenge(page, orgSlug, email);

    const codeInput = page.getByTestId("2fa-login-code");
    await expect(codeInput).toBeVisible();

    // Positive control for the two tests above: a deliberately wrong code
    // must produce a *code* error, not the pre-fix "Temporary token
    // required" plumbing error.
    const wrongCode = generateTotp(secret) === "000000" ? "000001" : "000000";
    await codeInput.fill(wrongCode);
    await page.getByTestId("2fa-login-verify").click();

    const error = page.getByTestId("login-error");
    await expect(error).toBeVisible();
    await expect(error).not.toContainText(/temporary token/i);
    expect(page.url()).toContain("/login");
  });
});
