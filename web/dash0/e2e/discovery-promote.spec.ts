import { test, expect } from "@playwright/test";

// Deterministic seed from server/test/testdata/testdata.go (SP_RUNMODE=test).
const SCAN_UID = "00000000-0000-0000-0000-000000000007";

test.describe("Discovery host promotion", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto("/dash0/orgs/test/login");
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();
    await page.waitForURL((url) => !url.pathname.includes("login"));
  });

  test("promotes a seeded host into a check end-to-end", async ({ page }) => {
    // Open the seeded scan detail.
    await page.goto(`/dash0/orgs/test/discovery/${SCAN_UID}`);

    // The seeded host appears as pending (not yet promoted).
    const hostRow = page.getByRole("row", { name: /127\.0\.0\.1/ });
    await expect(hostRow).toBeVisible();
    await expect(hostRow.getByText(/pending/i)).toBeVisible();

    // Open the promote form (guards the Outlet regression: this child route
    // must mount instead of re-rendering the scan detail).
    await hostRow.getByRole("link", { name: /promote/i }).click();
    await expect(
      page.getByRole("heading", { name: /promote to check/i }),
    ).toBeVisible();

    // Name is prefilled and the TCP suggested check is pre-ticked.
    await expect(page.locator("#name")).not.toHaveValue("");
    const tcpCheckbox = page.getByRole("checkbox", { name: "tcp" });
    await expect(tcpCheckbox).toBeChecked();

    // Submit and assert success + navigation back to the scan.
    await page.getByRole("button", { name: /create checks/i }).click();
    await expect(page.getByText(/host promoted to check/i)).toBeVisible();
    await page.waitForURL((url) => url.pathname.endsWith(`/discovery/${SCAN_UID}`));

    // The host now shows the promoted badge.
    await expect(
      page.getByRole("row", { name: /127\.0\.0\.1/ }).getByText(/promoted/i),
    ).toBeVisible();
  });
});
