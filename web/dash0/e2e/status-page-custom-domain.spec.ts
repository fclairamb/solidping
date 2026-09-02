import { test, expect, type Page } from "./fixtures";

// One status-page GET response, rewritten so the custom-domain card renders a
// verified domain in a chosen certificate state. The backend under test runs
// with in-server ACME disabled (there is no CA in the E2E environment), so the
// chip's three states can only be exercised by mocking the field the server
// would otherwise compute — everything below the API boundary is the real UI.
const MOCKED_DOMAIN = "status-cert-chip.example.com";

async function mockCertStatus(
  page: Page,
  certStatus: "issued" | "error" | "none",
) {
  await page.route(
    /\/api\/v1\/orgs\/[^/]+\/status-pages\/[^/?]+(\?|$)/,
    async (route) => {
      if (route.request().method() !== "GET") {
        await route.fallback();
        return;
      }

      const response = await route.fetch();
      let body: Record<string, unknown>;
      try {
        body = await response.json();
      } catch {
        await route.fulfill({ response });
        return;
      }

      // Only a single status page carries the custom-domain fields; leave any
      // other payload (collections, sub-resources) untouched.
      if (!body || typeof body !== "object" || !("uid" in body)) {
        await route.fulfill({ response });
        return;
      }

      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          ...body,
          customDomain: MOCKED_DOMAIN,
          customDomainStatus: "verified",
          customDomainCertStatus: certStatus,
          customDomainRecords: [
            {
              type: "CNAME",
              name: MOCKED_DOMAIN,
              value: "solidping.example.com",
            },
          ],
        }),
      });
    },
  );
}

// One status-page GET response, rewritten so the custom-domain card renders a
// chosen lifecycle state (spec 2026-08-23-03). The state is produced by the
// periodic DNS sweep, which cannot be driven from an E2E run, so the field the
// server would compute is mocked and everything below the API boundary is the
// real UI.
async function mockDomainState(
  page: Page,
  state: "grace" | "demoted",
  lastCheck: string,
) {
  await page.route(
    /\/api\/v1\/orgs\/[^/]+\/status-pages\/[^/?]+(\?|$)/,
    async (route) => {
      if (route.request().method() !== "GET") {
        await route.fallback();
        return;
      }

      const response = await route.fetch();
      let body: Record<string, unknown>;
      try {
        body = await response.json();
      } catch {
        await route.fulfill({ response });
        return;
      }

      if (!body || typeof body !== "object" || !("uid" in body)) {
        await route.fulfill({ response });
        return;
      }

      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          ...body,
          customDomain: MOCKED_DOMAIN,
          // `grace` is STILL verified — that is the whole point of the state.
          customDomainStatus: state === "grace" ? "verified" : "unverified",
          customDomainState: state,
          customDomainLastCheck: lastCheck,
        }),
      });
    },
  );
}

// openFirstStatusPageEditor navigates to the edit route of the first status
// page in the list.
async function openFirstStatusPageEditor(page: Page) {
  await page
    .getByTestId("app-sidebar")
    .getByRole("link", { name: "Status Pages" })
    .click();
  await page.waitForURL(/\/status-pages/);
  await page.waitForLoadState("networkidle");

  const editButton = page
    .locator('[data-testid^="status-page-row-edit-"]')
    .first();
  await expect(editButton).toBeVisible({ timeout: 10000 });
  await editButton.click();
  await page.waitForURL(/\/status-pages\/[^/]+\/edit/);
  await page.waitForLoadState("networkidle");
}

test.describe("Status page custom domain", () => {
  test("set a domain shows a single CNAME record and an unverified chip", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Open the status pages list.
    await page
      .getByTestId("app-sidebar")
      .getByRole("link", { name: "Status Pages" })
      .click();
    await page.waitForURL(/\/status-pages/);
    await page.waitForLoadState("networkidle");

    // Open the edit route for the first status page row.
    const editButton = page
      .locator('[data-testid^="status-page-row-edit-"]')
      .first();
    await expect(editButton).toBeVisible({ timeout: 10000 });
    await editButton.click();
    await page.waitForURL(/\/status-pages\/[^/]+\/edit/);
    await page.waitForLoadState("networkidle");

    // The custom-domain card renders.
    const card = page.getByTestId("status-page-custom-domain");
    await expect(card).toBeVisible({ timeout: 10000 });

    // Enter a domain that does not shadow any of the installation's own hosts
    // and save it.
    const domain = `status-e2e-${Date.now()}.example.com`;
    await card.getByTestId("custom-domain-input").fill(domain);
    await card.getByTestId("custom-domain-save").click();

    // v0.8.0 contract: exactly ONE DNS record, a CNAME. The TXT ownership
    // challenge is gone, so none of its markers may appear on the card.
    const records = card.getByTestId("custom-domain-records");
    await expect(records).toBeVisible({ timeout: 10000 });
    await expect(records).toContainText("CNAME");
    await expect(records).not.toContainText("_solidping-challenge");
    await expect(records).not.toContainText("sp-domain-verify");
    await expect(records).not.toContainText("TXT");

    // One record row => the "Type" label appears exactly once.
    await expect(records.getByText("Type", { exact: true })).toHaveCount(1);

    // The record is named after the customer's domain.
    await expect(records).toContainText(domain);

    // The value carries a copy-to-clipboard button.
    await expect(
      records.getByRole("button", { name: /copy record value/i }),
    ).toBeVisible();

    // Verify button and the unverified chip.
    await expect(card.getByTestId("custom-domain-verify")).toBeVisible();
    await expect(card).toContainText(/Unverified/i);

    // The certificate chip only appears once the domain is verified AND the
    // server terminates TLS itself, so it must be absent here.
    await expect(card.getByTestId("custom-domain-cert-status")).toHaveCount(0);

    await page.screenshot({
      path: "test-results/screenshots/status-page-custom-domain.png",
      fullPage: true,
    });
  });

  test("verifying a domain with no DNS keeps it unverified and certless", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page
      .getByTestId("app-sidebar")
      .getByRole("link", { name: "Status Pages" })
      .click();
    await page.waitForURL(/\/status-pages/);
    await page.waitForLoadState("networkidle");

    const editButton = page
      .locator('[data-testid^="status-page-row-edit-"]')
      .first();
    await expect(editButton).toBeVisible({ timeout: 10000 });
    await editButton.click();
    await page.waitForURL(/\/status-pages\/[^/]+\/edit/);
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("status-page-custom-domain");
    await expect(card).toBeVisible({ timeout: 10000 });

    const domain = `status-verify-${Date.now()}.example.com`;
    await card.getByTestId("custom-domain-input").fill(domain);
    await card.getByTestId("custom-domain-save").click();
    await expect(card.getByTestId("custom-domain-records")).toBeVisible({
      timeout: 10000,
    });

    // The domain has no CNAME in real DNS, so verification must fail: the chip
    // stays Unverified and no certificate chip appears (nothing was issued).
    await card.getByTestId("custom-domain-verify").click();
    await expect(card).toContainText(/Unverified/i, { timeout: 20000 });
    await expect(card.getByTestId("custom-domain-cert-status")).toHaveCount(0);
  });

  test("certificate chip renders the issued state", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockCertStatus(page, "issued");
    await openFirstStatusPageEditor(page);

    const card = page.getByTestId("status-page-custom-domain");
    await expect(card).toBeVisible({ timeout: 10000 });
    await expect(card).toContainText(MOCKED_DOMAIN);

    const chip = card.getByTestId("custom-domain-cert-status");
    await expect(chip).toBeVisible();
    await expect(chip).toHaveText(/HTTPS active/i);
    // Success styling, same token as the Verified chip next to it.
    await expect(chip).toHaveClass(/bg-status-ok\/15/);

    // Nothing is pending, so the "issued on first request" hint stays away.
    await expect(card).not.toContainText(/first HTTPS request/i);
  });

  test("certificate chip renders the error state", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockCertStatus(page, "error");
    await openFirstStatusPageEditor(page);

    const card = page.getByTestId("status-page-custom-domain");
    await expect(card).toBeVisible({ timeout: 10000 });

    const chip = card.getByTestId("custom-domain-cert-status");
    await expect(chip).toBeVisible();
    await expect(chip).toHaveText(/HTTPS failed/i);
    // A failure must read as destructive, not as a neutral chip.
    await expect(chip).toHaveClass(/bg-status-error\/15/);
  });

  test("certificate chip renders the pending state with its hint", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockCertStatus(page, "none");
    await openFirstStatusPageEditor(page);

    const card = page.getByTestId("status-page-custom-domain");
    await expect(card).toBeVisible({ timeout: 10000 });

    const chip = card.getByTestId("custom-domain-cert-status");
    await expect(chip).toBeVisible();
    await expect(chip).toHaveText(/HTTPS pending/i);
    await expect(chip).toHaveClass(/bg-secondary/);

    // A verified domain with nothing issued yet explains why: issuance is
    // on-demand, on the first HTTPS request.
    await expect(card).toContainText(/first HTTPS request/i);
  });

  test("a degrading domain reads as a warning, not as Verified", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockDomainState(
      page,
      "grace",
      "mode=shared expected=cname.solidping.io resolved=elsewhere.example.net ok=false",
    );
    await openFirstStatusPageEditor(page);

    const card = page.getByTestId("status-page-custom-domain");
    await expect(card).toBeVisible({ timeout: 10000 });

    // The page is still being served, but showing a plain green "Verified"
    // here would hide the only warning the operator gets before it goes dark.
    await expect(card.getByTestId("custom-domain-degraded")).toBeVisible();
    await expect(card).not.toContainText(/^Verified$/);

    const alert = card.getByTestId("custom-domain-health-alert");
    await expect(alert).toBeVisible();
    await expect(alert).toContainText(/still being served/i);
    // The diagnostic is on screen, so the fix does not need a log search.
    await expect(alert).toContainText("resolved=elsewhere.example.net");
  });

  test("a demoted domain says the page has stopped being served", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockDomainState(
      page,
      "demoted",
      "mode=shared expected=cname.solidping.io resolved=<none> ok=false error=no such host",
    );
    await openFirstStatusPageEditor(page);

    const card = page.getByTestId("status-page-custom-domain");
    await expect(card).toBeVisible({ timeout: 10000 });
    await expect(card.getByTestId("custom-domain-degraded")).toHaveCount(0);

    const alert = card.getByTestId("custom-domain-health-alert");
    await expect(alert).toBeVisible();
    await expect(alert).toContainText(/no longer serving/i);
    await expect(alert).toContainText("error=no such host");
  });
});
