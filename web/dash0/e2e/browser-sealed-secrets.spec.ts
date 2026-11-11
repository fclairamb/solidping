/**
 * Browser-sealed check secrets (spec 2026-11-08-01).
 *
 * A private location with a shared region key has a recipient without any
 * enrolled agent, which is what lets these specs run against a plain
 * side-car: the region key is generated in the browser (only `age1…` reaches
 * the server), then an HTTP basic-auth check sealed in the browser never sends
 * its password.
 */
import { test, expect, API_BASE, uniqueStamp, type Page } from "./fixtures";
import { choosePinnedRegions } from "./placement-helpers";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

// Private regions created in the shared `test` org. They are deleted after each
// test: a leftover one makes the org multi-region for every other spec (the
// check form's region picker, check-placement's single-region assertions).
const createdTestOrgRegions: string[] = [];

async function createPrivateRegion(page: Page, token: string, slug: string, org = "test"): Promise<void> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/${org}/private-regions`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { slug, name: `Sealed ${slug}` },
  });
  expect(resp.ok()).toBeTruthy();
  if (org === "test") createdTestOrgRegions.push(slug);
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
  // Wait for the form itself: the edit page shows a skeleton first, and the
  // section of a check with stored credentials opens on its own.
  await expect(page.getByTestId("section-authentication-trigger")).toBeVisible();
  if (!(await panel.isVisible())) await page.getByTestId("section-authentication-trigger").click();
  await expect(panel).toBeVisible();
}

/** Generates the region key from the private locations page (through the
 * generate button, or the key mode select) and returns the bodies of the
 * region-key requests the page sent. */
async function generateRegionKey(page: Page, slug: string, via: "button" | "select" = "button"): Promise<string[]> {
  const bodies: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes(`/private-regions/${slug}/region-key`)) {
      bodies.push(request.postData() ?? "");
    }
  });

  await page.goto("orgs/test/organization/private-locations");
  const cell = page.getByTestId(`region-key-${slug}`);
  await expect(cell).toBeVisible();
  if (via === "select") {
    await cell.getByTestId("region-key-mode-select").click();
    await page.getByTestId("region-key-mode-shared").click();
  } else {
    await cell.getByTestId("region-key-generate-button").click();
  }

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
  test.afterEach(async ({ authenticatedPage }) => {
    const token = await getAuthToken(authenticatedPage);
    for (const slug of createdTestOrgRegions.splice(0)) {
      await authenticatedPage.request.delete(`${API_BASE}/api/v1/orgs/test/private-regions/${slug}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
    }
  });

  test("seals an HTTP basic-auth password in the browser", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const slug = `seal-${Date.now().toString(36)}`.slice(0, 20);
    await createPrivateRegion(page, token, slug);
    await generateRegionKey(page, slug, "select");

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
    // The detail URL may carry a query string: rebuild the edit URL from the path.
    const detail = new URL(page.url());
    await page.goto(`${detail.origin}${detail.pathname}/edit`);
    await openAuthSection(page);
    await expect(page.getByTestId("sealed-secret-stored")).toBeVisible();
    // The blob is replaced whole: the secret inputs stay closed until
    // Replace opens them all at once.
    await expect(page.getByTestId("check-password-input")).toBeDisabled();
    await expect(page.getByTestId("check-username-input")).toBeDisabled();
    // Secret headers too: adding then removing a row would send
    // `secretHeaders: {}`, a clear that drops the sealed blob.
    await expect(page.getByTestId("add-secret-header-button")).toBeDisabled();
    await page.getByTestId("sealed-secret-replace-button").click();
    await expect(page.getByTestId("add-secret-header-button")).toBeEnabled();
    await expect(page.getByTestId("sealed-secret-replace-button")).toHaveCount(0);
    await expect(page.getByTestId("check-password-input")).toBeEnabled();
    await page.getByTestId("check-username-input").fill("alice");
    await page.getByTestId("check-password-input").fill(`${password}-2`);
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/(?!new)[^/]+$/);
    await expect(page.getByTestId("check-browser-sealed-badge")).toBeVisible();
    for (const body of checkBodies) {
      expect(body).not.toContain(password);
    }
  });

  // Spec 2026-11-11-01: sealed secrets are bound to the check's target, so
  // editing the URL of a sealed check opens the secret inputs to type again.
  test("editing the URL of a sealed check opens the secret inputs", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const slug = `bind-${Date.now().toString(36)}`.slice(0, 20);
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
    await page.getByTestId("check-name-input").fill(`E2E Bound ${Date.now()}`);
    await page.getByTestId("check-url-input").fill("https://internal.example.com/health");
    await choosePinnedRegions(page);
    await ensureRegionChecked(page, `region-option-@${slug}`);
    await ensureRegionUnchecked(page, "region-option-default");
    await openAuthSection(page);
    await page.getByTestId("check-username-input").fill("alice");
    await page.getByTestId("check-password-input").fill(password);
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/(?!new)[^/]+$/);
    await expect(page.getByTestId("check-browser-sealed-badge")).toBeVisible();

    const create = checkBodies.find((body) => body.includes("sealedSecrets"));
    expect(create, "the create request carries sealedSecrets").toBeTruthy();
    expect(JSON.parse(create ?? "{}").sealedSecrets.bound, "the blob is bound to its target").toBe(true);

    const detail = new URL(page.url());
    await page.goto(`${detail.origin}${detail.pathname}/edit`);
    await openAuthSection(page);
    await expect(page.getByTestId("check-password-input")).toBeDisabled();

    // A non-target edit keeps the inputs closed.
    await expect(page.getByTestId("sealed-target-changed")).toHaveCount(0);

    // A new URL opens them, with the reason.
    await page.getByTestId("check-url-input").fill("https://other.example.com/health");
    await expect(page.getByTestId("sealed-target-changed")).toBeVisible();
    await expect(page.getByTestId("check-password-input")).toBeEnabled();
    await expect(page.getByTestId("sealed-secret-replace-button")).toHaveCount(0);

    // Saving without the secrets is refused in the form: nothing is sent.
    const before = checkBodies.length;
    await page.getByTestId("check-submit-button").click();
    await expect(page.getByTestId("check-form-error")).toBeVisible();
    expect(checkBodies.length).toBe(before);

    // Typed again, they are sealed and bound to the new URL.
    await page.getByTestId("check-username-input").fill("alice");
    await page.getByTestId("check-password-input").fill(`${password}-2`);
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/(?!new)[^/]+$/);
    await expect(page.getByTestId("check-browser-sealed-badge")).toBeVisible();
    const patch = checkBodies.slice(before).find((body) => body.includes("sealedSecrets"));
    expect(patch, "the PATCH carries new sealed secrets").toBeTruthy();
    expect(JSON.parse(patch ?? "{}").sealedSecrets.bound).toBe(true);
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
    await page.getByTestId("check-username-input").fill("bob");
    await page.getByTestId("check-password-input").fill("typed-for-agents");

    // Adding a shared region switches to server mode, with a notice, and
    // empties what was typed for the agents.
    await ensureRegionChecked(page, "region-option-default");
    await expect(page.getByTestId("secret-encryption-panel")).toHaveAttribute("data-mode", "server");
    await expect(page.getByTestId("secret-encryption-switched-notice")).toBeVisible();
    await expect(page.getByTestId("secret-encryption-mode-browser")).toBeDisabled();
    await expect(page.getByTestId("check-password-input")).toHaveValue("");
    await expect(page.getByTestId("check-username-input")).toHaveValue("");

    await page.getByTestId("check-username-input").fill("bob");
    await page.getByTestId("check-password-input").fill("server-side");
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/(?!new)[^/]+$/);
    await expect(page.getByTestId("check-browser-sealed-badge")).toHaveCount(0);
  });

  test("required org: no server choice, shared regions and secrets exclude each other", async ({ page }) => {
    // A throwaway org: `required` must not leak into the shared `test` org
    // while other specs save plaintext secrets there.
    const stamp = uniqueStamp();
    const email = `sealreq-${stamp}@unknown.example`;
    const password = "Strong-Pass-123!";
    const user = await page.request.post(`${API_BASE}/api/v1/test/users`, {
      data: { email, password, name: "Sealing Required User" },
    });
    expect(user.status()).toBe(201);
    const login = await page.request.post(`${API_BASE}/api/v1/auth/login`, { data: { email, password } });
    const orgSlug = `sr-${stamp}`.slice(0, 30);
    const created = await page.request.post(`${API_BASE}/api/v1/orgs`, {
      headers: { Authorization: `Bearer ${(await login.json()).accessToken}` },
      data: { name: `Sealing Required ${stamp}`, slug: orgSlug },
    });
    expect(created.status()).toBe(201);
    const session = (await created.json()) as { accessToken: string; refreshToken?: string; expiresIn?: number };
    const auth = { Authorization: `Bearer ${session.accessToken}` };
    await page.addInitScript(
      ({ accessToken, refreshToken, expiresIn, slug }) => {
        localStorage.setItem("solidping_session_token", accessToken);
        if (refreshToken) localStorage.setItem("solidping_refresh_token", refreshToken);
        if (expiresIn) {
          localStorage.setItem("solidping_expires_at", String(Date.now() + expiresIn * 1000));
          localStorage.setItem("solidping_expires_in", String(expiresIn));
        }
        localStorage.setItem("solidping_org", slug);
      },
      {
        accessToken: session.accessToken,
        refreshToken: session.refreshToken ?? "",
        expiresIn: session.expiresIn ?? 0,
        slug: orgSlug,
      },
    );
    const regionSlug = `req-${stamp}`.slice(0, 20);
    await createPrivateRegion(page, session.accessToken, regionSlug, orgSlug);

    await page.goto(`orgs/${orgSlug}/organization/settings`);
    await expect(page.getByTestId("browser-sealing-card")).toBeVisible();
    await page.getByTestId("browser-sealing-mode-select").click();
    await page.getByTestId("browser-sealing-mode-required").click();
    await expect(page.getByTestId("browser-sealing-noncompliant")).toBeVisible();

    // Plaintext secrets are refused by the API.
    const refused = await page.request.post(`${API_BASE}/api/v1/orgs/${orgSlug}/checks`, {
      headers: auth,
      data: {
        type: "http", name: `E2E Required ${stamp}`, regions: ["default"],
        config: { url: "https://acme.com", username: "u", password: "p" },
      },
    });
    expect(refused.status()).toBe(400);
    expect((await refused.json()).code).toBe("SECRETS_MUST_BE_SEALED");

    await page.goto(`orgs/${orgSlug}/checks/new`);
    await page.getByTestId("check-url-input").fill("https://acme.com/required");
    await choosePinnedRegions(page);
    await ensureRegionChecked(page, "region-option-default");
    await openAuthSection(page);
    await expect(page.getByTestId("secret-encryption-mode-server")).toHaveCount(0);
    await expect(page.getByTestId("secret-encryption-mode-browser")).toBeVisible();

    // A shared region picked first closes the secret inputs, with the reason.
    await expect(page.getByTestId("check-password-input")).toBeDisabled();
    await expect(page.getByTestId("secret-inputs-locked")).toBeVisible();

    // Private regions only: the inputs open. A typed secret then disables
    // the shared regions in the picker.
    await ensureRegionChecked(page, `region-option-@${regionSlug}`);
    await ensureRegionUnchecked(page, "region-option-default");
    await expect(page.getByTestId("check-password-input")).toBeEnabled();
    await expect(page.getByTestId("region-checkbox-default")).toBeEnabled();
    await page.getByTestId("check-password-input").fill("sealed-only");
    await expect(page.getByTestId("region-checkbox-default")).toBeDisabled();
    await expect(page.getByTestId("region-option-default")).toHaveAttribute("title", /sealed in the browser/);
    // Positive control: the private region stays selectable.
    await expect(page.getByTestId(`region-checkbox-@${regionSlug}`)).toBeEnabled();
  });
});
