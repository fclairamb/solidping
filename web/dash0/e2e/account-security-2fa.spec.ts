import { test, expect, API_BASE } from "./fixtures";
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
    const stamp = Date.now() + Math.floor(Math.random() * 1000);
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

    const orgSlug = `a2f-${stamp.toString(36)}`;
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

  test("setup dialog renders the QR code, manual secret, and code input", async ({
    page,
  }) => {
    const { org } = await seedUserWithOrg(page);
    await seedBrowserSession(page, { ...org, slug: org.slug });

    await page.goto(`orgs/${org.slug}/account/security`);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("2fa-enable-button").click();

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

    const thirdPartyRequests: string[] = [];
    page.on("request", (req) => {
      if (req.url().includes("qrserver.com")) {
        thirdPartyRequests.push(req.url());
      }
    });

    await page.goto(`orgs/${org.slug}/account/security`);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("2fa-enable-button").click();
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
    await page.getByTestId("2fa-enable-button").click();

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
});
