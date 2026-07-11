import { test, expect } from "./fixtures";
import { expandSection } from "./section-helpers";

// Coverage for spec 2026-07-11-15: the check form uses progressive disclosure —
// less-common fields live in collapsed-by-default CollapsibleSections that must
// still (a) force-expand + reveal a field with a validation error on submit,
// (b) load expanded when they already hold non-default values, and (c) never
// hide the Save action behind the tuning knobs (sticky footer).
test.describe("Check form progressive disclosure", () => {
  test("force-expands a collapsed section that owns a validation error on submit", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // Organization (which holds the slug) is collapsed on a fresh create form.
    const orgTrigger = page.getByTestId("section-organization-trigger");
    await expect(orgTrigger).toHaveAttribute("data-state", "closed");

    // A valid target, so the only error is the slug we are about to break.
    await page.getByTestId("check-url-input").fill("https://example.com/pd-error");

    // Enter an invalid (too-short) slug, then collapse the section again.
    await expandSection(page, "section-organization-trigger");
    await page.getByTestId("check-slug-input").fill("ab");
    await orgTrigger.click();
    await expect(orgTrigger).toHaveAttribute("data-state", "closed");

    // Submitting must re-open Organization and surface the invalid slug — a
    // collapsible that swallowed the error would be worse than the wall.
    await page.getByTestId("check-submit-button").click();
    await expect(orgTrigger).toHaveAttribute("data-state", "open");
    await expect(page.getByTestId("check-slug-input")).toBeVisible();
    await expect(page.getByTestId("check-slug-input")).toHaveValue("ab");
    // Still on the create form (submission was blocked).
    await expect(page).toHaveURL(/\/checks\/new/);
  });

  test("loads a section expanded when it holds non-default values", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Pre-fill a non-default confirmation period via the query-param whitelist.
    await page.goto("orgs/test/checks/new?checkType=http&confirmationPeriod=300");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    // Incident tracking must open on load because it carries a non-default value.
    await expect(
      page.getByTestId("section-incident-tracking-trigger"),
    ).toHaveAttribute("data-state", "open");
    await expect(page.getByTestId("confirmation-period-input")).toHaveValue("300");

    // A section with only defaults stays collapsed.
    await expect(page.getByTestId("section-flapping-trigger")).toHaveAttribute(
      "data-state",
      "closed",
    );
  });

  test("deep-links a section open via ?section=", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=http&section=flapping");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await expect(page.getByTestId("section-flapping-trigger")).toHaveAttribute(
      "data-state",
      "open",
    );
  });

  test("saves via the sticky footer without expanding any section", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/new?checkType=http");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E Progressive ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page.getByTestId("check-url-input").fill("https://example.com/pd-save");

    // The submit button lives in the always-visible sticky footer — saving
    // never requires opening or scrolling past a collapsible section.
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: checkName })).toBeVisible();
  });
});
