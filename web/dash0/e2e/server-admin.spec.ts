import { test, expect, type Page } from "./fixtures";

async function navigateToServer(page: Page) {
  await page.getByTestId("user-menu-button").click();
  await page.getByTestId("server-settings-link").click();
  await page.waitForURL(/\/server\/web/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");
}

test.describe("Server Admin", () => {
  test("should show Server link in user menu for superadmin", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Open user dropdown
    await page.getByTestId("user-menu-button").click();

    // Superadmin user should see the "Server" link in the dropdown
    await expect(page.getByTestId("server-settings-link")).toBeVisible();
  });

  test("should navigate to server settings and display web tab", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Navigate via user menu
    await navigateToServer(page);

    // Verify the page heading
    await expect(
      page.getByRole("heading", { name: "Server Settings" }),
    ).toBeVisible();

    // Verify all tabs are visible
    await expect(page.getByRole("link", { name: "Web" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Mail", exact: true })).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Authentication" }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Performance" }),
    ).toBeVisible();

    // Verify web settings content
    await expect(page.getByLabel("Base URL")).toBeVisible();
    await expect(page.getByText("JWT Secret")).toBeVisible();
    await expect(page.getByRole("button", { name: "Save" })).toBeVisible();
  });

  test("should navigate between server tabs", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Navigate via user menu
    await navigateToServer(page);

    // Click Mail tab
    await page.getByRole("link", { name: "Mail", exact: true }).click();
    await page.waitForURL(/\/server\/mail/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByLabel("SMTP Host")).toBeVisible();
    await expect(page.getByLabel("From Address")).toBeVisible();

    // Click Authentication tab
    await page.getByRole("link", { name: "Authentication" }).click();
    await page.waitForURL(/\/server\/auth/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByText("Client ID", { exact: true }).first()).toBeVisible();
  });

  test("should save base URL setting", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Navigate via user menu
    await navigateToServer(page);

    // Fill in the base URL
    const baseUrlInput = page.getByLabel("Base URL");
    await baseUrlInput.fill("https://solidping.example.com");

    // Click Save
    await page.getByRole("button", { name: "Save" }).click();

    // Wait for success message
    await expect(page.getByText("Settings saved.")).toBeVisible({
      timeout: 10000,
    });
  });

  test("should display test email section on mail tab", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Navigate to server, then mail tab
    await navigateToServer(page);
    await page.getByRole("link", { name: "Mail", exact: true }).click();
    await page.waitForURL(/\/server\/mail/);
    await page.waitForLoadState("networkidle");

    // Verify test email section is visible
    await expect(page.getByLabel("Recipient Email")).toBeVisible();
    await expect(page.getByTestId("send-test-email-button")).toBeVisible();
  });
});
