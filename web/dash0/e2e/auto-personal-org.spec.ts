import { test, expect, API_BASE, type Page } from "./fixtures";

// Spec 2026-10-30-04: a new user who belongs to no organization gets a
// personal one (auth.auto_personal_org, on by default under
// SP_DEPLOYMENT_MODE=saas, off on self-hosted) and lands on its dashboard
// instead of /no-org. The suite's CI servers are self-hosted, so there this
// file pins "self-hosted still lands on /no-org"; against a SaaS side-car it
// pins the personal org and the colleague banner.
//
// Registration must be enabled at boot (SP_AUTH_REGISTRATION_EMAIL_PATTERN,
// see confirm-registration-no-org.spec.ts), like the CI E2E servers do.

const PASSWORD = "Strong-Pass-123!";

async function deploymentMode(page: Page): Promise<string> {
  const resp = await page.request.get(`${API_BASE}/api/v1/config`);
  expect(resp.status()).toBe(200);
  const body = (await resp.json()) as { deploymentMode?: string };

  return body.deploymentMode ?? "self-hosted";
}

// Drives the real register -> confirm flow, reading the confirmation link
// from the queued email job (the pending entry only stores its hash).
async function registerAndConfirm(page: Page, name: string, email: string) {
  await page.goto("orgs/test/register");
  await page.locator("#name").fill(name);
  await page.locator("#email").fill(email);
  await page.locator("#password").fill(PASSWORD);
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.getByTestId("register-confirmation-message")).toContainText(email, {
    timeout: 10000,
  });

  const jobsResp = await page.request.get(`${API_BASE}/api/v1/test/jobs?type=email`);
  expect(jobsResp.status()).toBe(200);
  const { data: jobs } = (await jobsResp.json()) as {
    data: Array<{
      config: { template?: string; to?: string[]; templateData?: { ConfirmURL?: string } };
    }>;
  };
  const job = jobs.find(
    (j) => j.config.template === "registration.html" && j.config.to?.includes(email),
  );
  const confirmURL = job?.config.templateData?.ConfirmURL;
  expect(confirmURL, "the registration email must have been queued").toBeTruthy();

  await page.goto(`confirm-registration/${(confirmURL as string).split("/").pop()}`);
}

test.describe("Personal organization at first sign-in", () => {
  test("a fresh signup lands on its personal org on SaaS, on /no-org on self-hosted", async ({
    page,
  }) => {
    const stamp = Date.now() % 1_000_000;
    const email = `personal-${stamp}@unknown.example`;
    const mode = await deploymentMode(page);

    await registerAndConfirm(page, `Pat${stamp} Tester`, email);

    if (mode === "saas") {
      // "Pat123456's organization", slug from the first name.
      await page.waitForURL(new RegExp(`/orgs/pat${stamp}\\d*(/|$|\\?)`), { timeout: 15000 });
      await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 10000 });
      expect(page.url()).not.toContain("/no-org");

      return;
    }

    await page.waitForURL(/\/no-org(\?|$)/, { timeout: 10000 });
    await expect(page.getByTestId("no-org-advanced-toggle")).toBeVisible({ timeout: 10000 });
  });

  test("the personal org offers to join the org colleagues already use", async ({ page }) => {
    test.skip((await deploymentMode(page)) !== "saas", "auto personal orgs are a SaaS default");

    const stamp = Date.now() % 1_000_000;
    const domain = `acme-${stamp}.example`;
    const orgName = `Acme Corp ${stamp}`;

    // A colleague, with an org made by hand (their own personal org does
    // not count for the hint).
    const bobEmail = `bob@${domain}`;
    const seed = await page.request.post(`${API_BASE}/api/v1/test/users`, {
      data: { email: bobEmail, password: PASSWORD, name: "Bob Builder" },
    });
    expect(seed.status()).toBe(201);
    const login = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { email: bobEmail, password: PASSWORD },
    });
    expect(login.status()).toBe(200);
    const { accessToken } = (await login.json()) as { accessToken: string };
    const created = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      data: { name: orgName },
      headers: { Authorization: `Bearer ${accessToken}` },
    });
    expect(created.status()).toBe(201);

    await registerAndConfirm(page, `Ali${stamp} Doe`, `alice@${domain}`);
    await page.waitForURL(new RegExp(`/orgs/ali${stamp}\\d*(/|$|\\?)`), { timeout: 15000 });

    const banner = page.getByTestId("colleague-hint-banner");
    await expect(banner).toBeVisible({ timeout: 10000 });
    await expect(banner).toContainText(domain);
    await expect(banner).toContainText(orgName);

    await page.getByTestId("colleague-hint-request").click();
    await expect(page.getByTestId("colleague-hint-sent")).toBeVisible();

    // Asked once: the banner has nothing left to offer.
    await page.reload();
    await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId("colleague-hint-banner")).toHaveCount(0);

    // Another colleague dismisses it, for good.
    await page.context().clearCookies();
    await page.evaluate(() => window.localStorage.clear());
    await registerAndConfirm(page, `Cam${stamp} Roe`, `cam@${domain}`);
    await page.waitForURL(new RegExp(`/orgs/cam${stamp}\\d*(/|$|\\?)`), { timeout: 15000 });
    await expect(page.getByTestId("colleague-hint-banner")).toBeVisible({ timeout: 10000 });
    await page.getByTestId("colleague-hint-dismiss").click();
    await expect(page.getByTestId("colleague-hint-banner")).toHaveCount(0);
    await page.reload();
    await expect(page.getByTestId("app-sidebar")).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId("colleague-hint-banner")).toHaveCount(0);
  });
});
