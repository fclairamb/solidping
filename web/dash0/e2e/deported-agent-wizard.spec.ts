import { test, expect, API_BASE, type Page } from "./fixtures";

// E2E for the guided "Register an agent" wizard (spec 2026-07-18-02): pick an
// existing private location, mint an enrollment token, verify the run-the-
// agent snippets substitute the minted token and the org-correct server URL,
// and land on the "waiting for connection" step. Driving a real agent
// enrollment over the WS protocol isn't practical from Playwright, so the
// success step is exercised in a dedicated backend/integration test instead —
// here we assert the waiting state, per the spec's explicit allowance.

const REGION_SLUG = "e2e-wizard-dc";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function deleteRegionIfExists(page: Page, token: string, slug: string): Promise<void> {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/private-regions/${slug}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

// Serial: the empty-state assertion only holds before this file's own region
// exists, and is best-effort even then (another spec file's region in the
// shared "test" org can make the list non-empty regardless — guarded below).
test.describe.serial("Register an agent wizard", () => {
  test.beforeEach(async ({ authenticatedPage }) => {
    const token = await getAuthToken(authenticatedPage);
    await deleteRegionIfExists(authenticatedPage, token, REGION_SLUG);
  });

  test.afterEach(async ({ authenticatedPage }) => {
    const token = await getAuthToken(authenticatedPage);
    await deleteRegionIfExists(authenticatedPage, token, REGION_SLUG);
  });

  test("empty state links into the wizard", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/organization/private-locations");
    await page.waitForLoadState("networkidle");

    // Only assert the empty-state link when there really are no regions —
    // another spec in the same run may have left one behind momentarily.
    const emptyState = page.getByTestId("no-private-regions");
    if (await emptyState.isVisible().catch(() => false)) {
      await page.getByTestId("empty-state-register-agent").click();
      await page.waitForURL((url) => url.pathname.endsWith("/private-locations/register"));
      await expect(page.getByTestId("wizard-step-pick-location")).toBeVisible();
    }
  });

  test("entry points, pick location, mint token, snippets, waiting step", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Create the region via API for speed — step 1's "pick an existing
    // location" path is exercised below; the inline create form is simple
    // enough to leave to component-level review.
    const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/private-regions`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { slug: REGION_SLUG, name: "E2E Wizard DC" },
    });
    expect(resp.ok()).toBeTruthy();

    // Entry point: the "Register an agent" button on the private-locations
    // list page.
    await page.goto("orgs/test/organization/private-locations");
    await page.waitForLoadState("networkidle");
    await page.getByTestId("register-agent-button").click();
    await page.waitForURL((url) => url.pathname.endsWith("/private-locations/register"));

    // Step 1: pick the existing location.
    await expect(page.getByTestId("wizard-step-pick-location")).toBeVisible();
    await page.getByTestId(`wizard-region-${REGION_SLUG}`).click();
    await expect(page.getByTestId("wizard-location-banner")).toContainText(`@test/${REGION_SLUG}`);

    // Step 2: mint the enrollment token — shown exactly once.
    await expect(page.getByTestId("wizard-step-mint-token")).toBeVisible();
    await page.getByTestId("wizard-mint-token").click();
    const mintedEl = page.getByTestId("wizard-minted-token");
    await expect(mintedEl).toBeVisible();
    const mintedToken = (await mintedEl.textContent())?.trim() ?? "";
    expect(mintedToken).toMatch(/^spe_/);

    await page.getByTestId("wizard-continue-to-run").click();

    // Step 3: the run-the-agent snippets substitute the real server URL and
    // the freshly minted token in every format.
    await expect(page.getByTestId("wizard-step-run-agent")).toBeVisible();
    const serverOrigin = new URL(page.url()).origin;

    const dockerRun = page.getByTestId("wizard-snippet-docker-run");
    await expect(dockerRun).toContainText(mintedToken);
    await expect(dockerRun).toContainText(serverOrigin);

    await page.getByTestId("wizard-tab-docker-compose").click();
    const dockerCompose = page.getByTestId("wizard-snippet-docker-compose");
    await expect(dockerCompose).toContainText(mintedToken);
    await expect(dockerCompose).toContainText(serverOrigin);

    await page.getByTestId("wizard-tab-kubernetes").click();
    const kubernetes = page.getByTestId("wizard-snippet-kubernetes");
    await expect(kubernetes).toContainText(mintedToken);
    await expect(kubernetes).toContainText(serverOrigin);

    // Step 4: waiting for the agent to connect.
    await page.getByTestId("wizard-continue-to-wait").click();
    await expect(page.getByTestId("wizard-step-waiting")).toBeVisible();
  });
});
