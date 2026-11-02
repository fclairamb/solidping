import { test, expect, API_BASE, uniqueStamp } from "./fixtures";
import { generateTotp } from "./totp-utils";

// Spec 2026-08-18-11: the TOTP setup dialog opened with only its title and a
// permanently-disabled Confirm — no QR code, no manual secret, no code
// field — because the frontend read a field the API doesn't return
// (`resp.qrCodeUrl` instead of `resp.uri`), so the render gate stayed false
// forever. Fixed by reading `uri`, rendering the QR entirely client-side
// (the `qrcode` package), and un-gating the manual secret / code input from
// the QR image.
//
// Every test here seeds a throwaway user + org (the account-password
// pattern) rather than the shared `test` fixture: enabling 2FA on the
// shared test@test.com user would lock every other spec in the suite out of
// its login.
test.describe("Account > Security > 2FA setup", () => {
  const PASSWORD = "Totp-Setup-123!";

  async function seedUserWithOrg(page: import("@playwright/test").Page) {
    const stamp = uniqueStamp();
    const email = `acct-2fa-${stamp}@unknown.example`;

    const createUserResp = await page.request.post(
      `${API_BASE}/api/v1/test/users`,
      { data: { email, password: PASSWORD, name: "Acct 2FA User" } },
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

    const orgSlug = `a2f-${stamp}`;
    const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${login.accessToken}` },
      data: { name: `Acct 2FA Co ${stamp}`, slug: orgSlug },
    });
    expect(createOrgResp.status()).toBe(201);
    const org = (await createOrgResp.json()) as {
      slug: string;
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
    };

    return { email, org };
  }

  async function seedBrowserSession(
    page: import("@playwright/test").Page,
    session: {
      accessToken: string;
      refreshToken?: string;
      expiresIn?: number;
      slug?: string;
    },
  ) {
    await page.addInitScript(
      ({ accessToken, refreshToken, expiresIn, slug }) => {
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
        if (slug) {
          localStorage.setItem("solidping_org", slug as string);
        }
      },
      {
        accessToken: session.accessToken,
        refreshToken: session.refreshToken ?? "",
        expiresIn: session.expiresIn ?? 0,
        slug: session.slug ?? "",
      },
    );
  }

  // Spec 2026-10-31-02: the secret is only handed out after the current
  // password. Opens the setup dialog and passes that step.
  async function openSetupWithPassword(page: import("@playwright/test").Page) {
    await page.getByTestId("2fa-enable-button").click();
    await page.getByTestId("2fa-current-password").fill(PASSWORD);
    await page.getByTestId("2fa-reauth-continue").click();
  }

  test("setup asks for the current password: a wrong one is refused, the right one goes through", async ({
    page,
  }) => {
    const { org } = await seedUserWithOrg(page);
    await seedBrowserSession(page, { ...org, slug: org.slug });

    await page.goto(`orgs/${org.slug}/account/security`);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("2fa-enable-button").click();

    // No secret before re-authentication.
    await expect(page.getByTestId("2fa-current-password")).toBeVisible();
    await expect(page.getByTestId("2fa-manual-secret")).toHaveCount(0);

    await page.getByTestId("2fa-current-password").fill("not-my-password");
    await page.getByTestId("2fa-reauth-continue").click();
    await expect(page.getByTestId("2fa-reauth-error")).toContainText(
      /not your current password/i,
    );
    await expect(page.getByTestId("2fa-manual-secret")).toHaveCount(0);
    // A wrong password in the body is not a dead session: still signed in.
    await expect(page).toHaveURL(/account\/security/);

    await page.getByTestId("2fa-current-password").fill(PASSWORD);
    await page.getByTestId("2fa-reauth-continue").click();
    await expect(page.getByTestId("2fa-manual-secret")).not.toHaveValue("");
  });

  test("the API refuses 2FA setup without the current password", async ({ page }) => {
    const { org } = await seedUserWithOrg(page);
    const auth = { Authorization: `Bearer ${org.accessToken}` };

    const noPassword = await page.request.post(`${API_BASE}/api/v1/auth/2fa/setup`, {
      headers: auth,
    });
    expect(noPassword.status()).toBe(401);
    expect(((await noPassword.json()) as { code: string }).code).toBe(
      "INVALID_CURRENT_PASSWORD",
    );

    const noPasskey = await page.request.post(
      `${API_BASE}/api/v1/auth/passkeys/register/begin`,
      { headers: auth, data: {} },
    );
    // 401 when WebAuthn is on, 503 when this server has it off: never 200.
    expect([401, 503]).toContain(noPasskey.status());
  });

  test("adding a passkey asks for the current password first", async ({ page }) => {
    const { org } = await seedUserWithOrg(page);
    await seedBrowserSession(page, { ...org, slug: org.slug });

    await page.goto(`orgs/${org.slug}/account/security`);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("passkey-add-button").click();

    await expect(page.getByTestId("passkey-reauth-dialog")).toBeVisible();
    await page.getByTestId("passkey-current-password").fill("not-my-password");
    await page.getByTestId("passkey-reauth-continue").click();
    await expect(page.getByTestId("passkey-reauth-error")).toBeVisible();
  });

  test("setup dialog renders the QR code, manual secret, and code input", async ({
    page,
  }) => {
    const { org } = await seedUserWithOrg(page);
    await seedBrowserSession(page, { ...org, slug: org.slug });

    await page.goto(`orgs/${org.slug}/account/security`);
    await page.waitForLoadState("networkidle");

    await openSetupWithPassword(page);

    // Fails on the pre-fix code: the whole body stayed hidden because
    // `qrCodeUrl` was always empty.
    const qr = page.getByTestId("2fa-qr-code");
    await expect(qr).toBeVisible();
    // Rendered client-side (data URL) — never a request to a third party.
    await expect(qr).toHaveAttribute("src", /^data:image\//);

    const secretInput = page.getByTestId("2fa-manual-secret");
    await expect(secretInput).toBeVisible();
    await expect(secretInput).not.toHaveValue("");

    await expect(page.getByTestId("2fa-setup-code")).toBeVisible();
  });

  test("no network request for the QR code leaves the page", async ({ page }) => {
    const { org } = await seedUserWithOrg(page);
    await seedBrowserSession(page, { ...org, slug: org.slug });

    // The assertion is "no request left the page", not "no request went to
    // qrserver.com": a substring match on one vendor's hostname would pass
    // just as happily if the QR generator were swapped for a different third
    // party, and a URL substring is fooled by a same-looking path
    // (…/?u=https://qrserver.com/). Compare parsed origins instead.
    // API_BASE is the origin the dashboard itself is served from in this
    // suite (see fixtures.ts), so it is what "the page's own origin" means.
    const appOrigin = new URL(API_BASE).origin;
    const thirdPartyRequests: string[] = [];
    page.on("request", (req) => {
      let origin: string;
      try {
        origin = new URL(req.url()).origin;
      } catch {
        return; // data:/blob: carry no origin and never leave the page
      }

      if (origin !== appOrigin && origin !== "null") {
        thirdPartyRequests.push(req.url());
      }
    });

    await page.goto(`orgs/${org.slug}/account/security`);
    await page.waitForLoadState("networkidle");
    await openSetupWithPassword(page);
    await expect(page.getByTestId("2fa-qr-code")).toBeVisible();

    expect(thirdPartyRequests).toEqual([]);
  });

  test("confirming the code enrolls 2FA and shows recovery codes", async ({
    page,
  }) => {
    const { org } = await seedUserWithOrg(page);
    await seedBrowserSession(page, { ...org, slug: org.slug });

    await page.goto(`orgs/${org.slug}/account/security`);
    await page.waitForLoadState("networkidle");
    await openSetupWithPassword(page);

    const secretInput = page.getByTestId("2fa-manual-secret");
    await expect(secretInput).toBeVisible();
    const secret = await secretInput.inputValue();
    expect(secret.length).toBeGreaterThan(0);

    const totpCode = generateTotp(secret);
    await page.getByTestId("2fa-setup-code").fill(totpCode);
    await page.getByTestId("2fa-setup-confirm").click();

    await expect(page.getByTestId("2fa-recovery-codes")).toBeVisible();
    const codesText = await page.getByTestId("2fa-recovery-codes").innerText();
    expect(codesText.trim().length).toBeGreaterThan(0);

    // Acknowledge and finish — the status badge flips to Enabled.
    await page.getByTestId("2fa-recovery-saved-checkbox").click();
    await page.getByTestId("2fa-recovery-done").click();
    await expect(page.getByTestId("2fa-status")).toHaveText(/Enabled/i);
  });

  // Spec 2026-10-27-02: DELETE /auth/2fa spends the same per-user attempt
  // budget as the login challenge. Once it is spent, the dialog says to wait
  // (translated) instead of showing a raw failure, and 2FA stays enabled.
  test("disabling 2FA is throttled after 5 wrong codes", async ({ page }) => {
    const { org } = await seedUserWithOrg(page);
    const auth = { Authorization: `Bearer ${org.accessToken}` };

    const setupResp = await page.request.post(`${API_BASE}/api/v1/auth/2fa/setup`, {
      headers: auth,
      data: { currentPassword: PASSWORD },
    });
    expect(setupResp.status()).toBe(200);
    const { secret, setupToken } = (await setupResp.json()) as {
      secret: string;
      setupToken: string;
    };
    const confirmResp = await page.request.post(`${API_BASE}/api/v1/auth/2fa/confirm`, {
      headers: auth,
      data: { code: generateTotp(secret), setupToken },
    });
    expect(confirmResp.status()).toBe(200);

    await seedBrowserSession(page, { ...org, slug: org.slug });
    await page.goto(`orgs/${org.slug}/account/security`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("2fa-status")).toHaveText(/Enabled/i);

    await page.getByTestId("2fa-disable-button").click();
    const codeInput = page.getByTestId("2fa-disable-code");
    const confirm = page.getByTestId("2fa-disable-confirm");
    const error = page.getByTestId("2fa-disable-error");
    const wrongCode = generateTotp(secret) === "000000" ? "000001" : "000000";

    // Positive control: the first wrong codes get the ordinary answer.
    for (let i = 0; i < 5; i++) {
      await codeInput.fill(wrongCode);
      await confirm.click();
      await expect(error).toBeVisible();
      await expect(error).not.toContainText(/too many attempts/i);
    }

    // Even the valid code is refused once the budget is spent.
    await codeInput.fill(generateTotp(secret));
    await confirm.click();
    await expect(error).toContainText(/too many attempts/i);

    await page.keyboard.press("Escape");
    await page.reload();
    await expect(page.getByTestId("2fa-status")).toHaveText(/Enabled/i);
  });
});
