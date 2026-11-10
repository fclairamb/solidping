/**
 * Browser-sealed check secrets (spec 2026-11-08-01).
 *
 * A private location with a shared region key has a recipient without any
 * enrolled agent, which is what lets these specs run against a plain
 * side-car: the region key is generated in the browser (only `age1…` reaches
 * the server), then an HTTP basic-auth check sealed in the browser never sends
 * its password.
 */
import { test, expect, API_BASE, type Page } from "./fixtures";
import { choosePinnedRegions } from "./placement-helpers";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function createPrivateRegion(page: Page, token: string, slug: string): Promise<void> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/private-regions`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { slug, name: `Sealed ${slug}` },
  });
  expect(resp.ok()).toBeTruthy();
}

async function ensureRegionChecked(page: Page, regionTestId: string): Promise<void> {
  const option = page.getByTestId(regionTestId);
  if (!(await option.getByRole("checkbox").isChecked())) await option.click();
}

async function ensureRegionUnchecked(page: Page, regionTestId: string): Promise<void> {
  const option = page.getByTestId(regionTestId);
  if ((await option.count()) === 0) return;
  if (await option.getByRole("checkbox").isChecked()) await option.click();
}

/** Opens the authentication section unless it already is. */
async function openAuthSection(page: Page): Promise<void> {
  const panel = page.getByTestId("check-username-input");
  if (!(await panel.isVisible())) await page.getByTestId("section-authentication-trigger").click();
  await expect(panel).toBeVisible();
}

/** Generates the region key from the private locations page and returns the
 * bodies of the region-key requests the page sent. */
async function generateRegionKey(page: Page, slug: string): Promise<string[]> {
  const bodies: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes(`/private-regions/${slug}/region-key`)) {
      bodies.push(request.postData() ?? "");
    }
  });

  await page.goto("orgs/test/organization/private-locations");
  const cell = page.getByTestId(`region-key-${slug}`);
  await expect(cell).toBeVisible();
  await cell.getByTestId("region-key-generate-button").click();

  const secret = page.getByTestId("region-key-secret");
  await expect(secret).toContainText("AGE-SECRET-KEY-1");
  const identity = (await secret.textContent()) ?? "";

  await page.getByTestId("region-key-confirm").click();
  await expect(page.getByTestId(`region-key-mode-${slug}`)).toContainText("Shared region key");
  await expect(page.getByTestId("region-key-dialog")).toHaveCount(0);

  // The secret is shown once and never sent: the request carries the
  // recipient only.
  expect(bodies.length).toBeGreaterThan(0);
  for (const body of bodies) {
    expect(body).toContain('"recipient":"age1');
    expect(body).not.toContain(identity.trim());
    expect(body).not.toContain("AGE-SECRET-KEY");
  }
  return bodies;
}

test.describe("Browser-sealed check secrets", () => {
  test("seals an HTTP basic-auth password in the browser", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const slug = `seal-${Date.now().toString(36)}`.slice(0, 20);
    await createPrivateRegion(page, token, slug);
    await generateRegionKey(page, slug);

    const password = `pw-${Date.now()}`;
    const checkBodies: string[] = [];
    page.on("request", (request) => {
      if (request.url().includes("/api/v1/orgs/test/checks") && ["POST", "PATCH"].includes(request.method())) {
        checkBodies.push(request.postData() ?? "");
      }
    });

    await page.goto("orgs/test/checks/new");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    const name = `E2E Sealed ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(name);
    await page.getByTestId("check-url-input").fill("https://internal.example.com/health");
    await choosePinnedRegions(page);
    await ensureRegionChecked(page, `region-option-@${slug}`);
    await ensureRegionUnchecked(page, "region-option-default");

    await openAuthSection(page);
    await expect(page.getByTestId("secret-encryption-panel")).toHaveAttribute("data-mode", "browser");
    await expect(page.getByTestId("sealed-secret-badge")).toContainText("region key");
    await page.getByTestId("check-username-input").fill("alice");
    await page.getByTestId("check-password-input").fill(password);

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/(?!new)[^/]+$/);
    await expect(page.getByTestId("check-browser-sealed-badge")).toBeVisible();

    // The plaintext never left the browser: the create carried a sealed blob.
    const create = checkBodies.find((body) => body.includes("sealedSecrets"));
    expect(create, "the create request carries sealedSecrets").toBeTruthy();
    for (const body of checkBodies) {
      expect(body).not.toContain(password);
    }

    // Reopen, replace every secret at once, save: still browser-sealed.
    const checkUrl = page.url();
    await page.goto(`${checkUrl}/edit`);
    await openAuthSection(page);
    await expect(page.getByTestId("sealed-secret-stored")).toBeVisible();
    await page.getByTestId("check-username-input").fill("alice");
    await page.getByTestId("check-password-input").fill(`${password}-2`);
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/(?!new)[^/]+$/);
    await expect(page.getByTestId("check-browser-sealed-badge")).toBeVisible();
    for (const body of checkBodies) {
      expect(body).not.toContain(password);
    }
  });

  test("falls back to server encryption on a shared region", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const slug = `mix-${Date.now().toString(36)}`.slice(0, 20);
    await createPrivateRegion(page, token, slug);
    await generateRegionKey(page, slug);

    await page.goto("orgs/test/checks/new");
    await page.getByTestId("check-name-input").fill(`E2E Server Mode ${Date.now()}`);
    await page.getByTestId("check-url-input").fill("https://example.com/server-mode");
    await choosePinnedRegions(page);
    await ensureRegionChecked(page, `region-option-@${slug}`);
    await ensureRegionUnchecked(page, "region-option-default");
    await openAuthSection(page);
    await expect(page.getByTestId("secret-encryption-panel")).toHaveAttribute("data-mode", "browser");

    // Adding a shared region switches to server mode, with a notice.
    await ensureRegionChecked(page, "region-option-default");
    await expect(page.getByTestId("secret-encryption-panel")).toHaveAttribute("data-mode", "server");
    await expect(page.getByTestId("secret-encryption-switched-notice")).toBeVisible();
    await expect(page.getByTestId("secret-encryption-mode-browser")).toBeDisabled();

    await page.getByTestId("check-username-input").fill("bob");
    await page.getByTestId("check-password-input").fill("server-side");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/(?!new)[^/]+$/);
    await expect(page.getByTestId("check-browser-sealed-badge")).toHaveCount(0);
  });

  test("required org: no server choice", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const auth = { Authorization: `Bearer ${token}` };

    await page.goto("orgs/test/organization/settings");
    await expect(page.getByTestId("browser-sealing-card")).toBeVisible();
    await page.getByTestId("browser-sealing-mode-select").click();
    await page.getByTestId("browser-sealing-mode-required").click();
    await expect(page.getByTestId("browser-sealing-noncompliant")).toBeVisible();

    try {
      // Plaintext secrets are refused by the API.
      const refused = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
        headers: auth,
        data: {
          type: "http", name: `E2E Required ${Date.now()}`, regions: ["default"],
          config: { url: "https://example.com", username: "u", password: "p" },
        },
      });
      expect(refused.status()).toBe(400);
      expect((await refused.json()).code).toBe("SECRETS_MUST_BE_SEALED");

      await page.goto("orgs/test/checks/new");
      await page.getByTestId("check-url-input").fill("https://example.com/required");
      await openAuthSection(page);
      await expect(page.getByTestId("secret-encryption-mode-server")).toHaveCount(0);
      await expect(page.getByTestId("secret-encryption-mode-browser")).toBeVisible();
    } finally {
      await page.request.patch(`${API_BASE}/api/v1/orgs/test/settings`, {
        headers: auth,
        data: { browserSealingMode: "optional" },
      });
    }
  });
});
