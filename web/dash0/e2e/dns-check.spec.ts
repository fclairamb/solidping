import { test, expect } from "./fixtures";
import { expandSection } from "./section-helpers";

// Regression coverage for spec 2026-07-10-11: the DNS check form used to submit
// the queried domain under the `domain` key (which the DNS backend ignores) and
// never populated the Domain field from samples (which carry `host`). It also
// had no way to specify a custom resolver. These tests exercise the fixed flow.
test.describe("DNS check form", () => {
  test("loads a DNS sample into the Domain field and submits successfully", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Deep-link straight to the DNS new-check form.
    await page.goto("orgs/test/checks/new?checkType=dns");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // The Domain field is DNS-specific and must render for this type.
    await expect(page.getByTestId("check-domain-input")).toBeVisible();

    // Open the sample picker and load the plain "Google DNS A Record" sample.
    await page.getByTestId("check-load-template-button").click();
    await page.getByTestId("check-sample-dns-google").click();

    // The core bug: the sample must fill the Domain field (from the `host` key),
    // not leave it empty.
    await expect(page.getByTestId("check-domain-input")).toHaveValue("google.com");

    // Override name/slug so we don't collide with the seeded sample check.
    const suffix = Date.now();
    await page.getByTestId("check-name-input").fill(`E2E DNS Sample ${suffix}`);
    await expandSection(page, "section-organization-trigger");
    await page.getByTestId("check-slug-input").fill(`e2e-dns-sample-${suffix}`);

    // Submit — should succeed (previously failed with "host is required").
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: `E2E DNS Sample ${suffix}` })).toBeVisible();
  });

  test("creates a DNS check with an explicit server and record type, round-tripping on edit", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=dns");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // DNS-specific inputs are present.
    await expect(page.getByTestId("check-dns-nameserver-input")).toBeVisible();
    await expect(page.getByTestId("check-dns-record-type-select")).toBeVisible();

    const suffix = Date.now();
    const checkName = `E2E DNS Server ${suffix}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await expandSection(page, "section-organization-trigger");
    await page.getByTestId("check-slug-input").fill(`e2e-dns-server-${suffix}`);
    await page.getByTestId("check-domain-input").fill("example.com");
    await page.getByTestId("check-dns-nameserver-input").fill("8.8.8.8:53");

    // Pick a non-default record type so it must be persisted (not discarded).
    await page.getByTestId("check-dns-record-type-select").click();
    await page.getByRole("option", { name: "AAAA" }).click();

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();

    // Edit round-trip: domain, server, and record type all persist.
    await page
      .getByTestId("check-detail-header")
      .getByRole("link", { name: "Edit" }).click();
    await page.waitForURL(/\/edit/);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("check-domain-input")).toHaveValue("example.com");
    await expect(page.getByTestId("check-dns-nameserver-input")).toHaveValue("8.8.8.8:53");
    await expect(page.getByTestId("check-dns-record-type-select")).toContainText("AAAA");
  });

  test("shows the DNS 'host is required' error inline on the Domain field", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=dns");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-domain-input")).toBeVisible();

    // Submit with an empty Domain — the form guards and stays on /new.
    await page.getByTestId("check-name-input").fill(`E2E DNS Empty ${Date.now()}`);
    await page.getByTestId("check-submit-button").click();
    await page.waitForTimeout(500);
    expect(page.url()).toContain("/checks/new");
    await expect(page.getByText(/Domain is required/i)).toBeVisible();
  });
});
