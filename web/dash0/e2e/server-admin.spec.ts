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

// navigateToHashing opens Server Settings and lands on the Password Hashing tab.
async function navigateToHashing(page: Page) {
  await navigateToServer(page);
  await page.getByRole("link", { name: "Password Hashing" }).click();
  await page.waitForURL(/\/server\/hashing/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");
}

// selectAlgorithm switches the Radix algorithm Select to the given option and
// waits for the matching cost inputs to render. Each test sets the algorithm it
// needs explicitly rather than relying on the persisted value, since these tests
// write the shared auth.password.* system parameters.
async function selectAlgorithm(page: Page, name: "argon2id" | "bcrypt") {
  await page.getByTestId("hashing-algorithm").click();
  // The option label includes a suffix for argon2id ("argon2id (recommended)"),
  // so match by the leading id.
  await page.getByRole("option", { name, exact: false }).first().click();
}

// These tests persist the shared auth.password.* system parameters on a single
// side-car server, so they must run serially to avoid stomping each other.
test.describe.configure({ mode: "serial" });

test.describe("Server Admin — Password Hashing", () => {
  test("super-admin can open the Hashing tab and sees the restart notice", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    await navigateToServer(page);
    await expect(
      page.getByRole("link", { name: "Password Hashing" }),
    ).toBeVisible();

    await navigateToHashing(page);

    // The algorithm select and the restart notice must be present.
    await expect(page.getByTestId("hashing-algorithm")).toBeVisible();
    await expect(page.getByText(/take effect after a server restart/i)).toBeVisible();
  });

  test("argon2 preset fills the cost inputs", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");
    await navigateToHashing(page);

    // Explicitly select argon2id so the preset buttons are shown regardless of
    // any previously-persisted algorithm.
    await selectAlgorithm(page, "argon2id");

    await expect(page.getByTestId("hashing-preset-owasp")).toBeVisible();
    await page.getByTestId("hashing-preset-owasp").click();

    // The OWASP profile is 19 MiB / t2 / p1.
    await expect(page.getByTestId("hashing-argon2-memory")).toHaveValue("19456");
    await expect(page.getByTestId("hashing-argon2-time")).toHaveValue("2");
    await expect(page.getByTestId("hashing-argon2-threads")).toHaveValue("1");
  });

  test("out-of-range bcrypt cost is rejected before saving", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");
    await navigateToHashing(page);

    await selectAlgorithm(page, "bcrypt");

    const cost = page.getByTestId("hashing-bcrypt-cost");
    await expect(cost).toBeVisible();
    // 99 is outside the accepted [10,31] range. The numeric input carries the
    // same min/max bounds the backend enforces, so the browser blocks the submit
    // with a native validity error and no save round-trips. (The API layer's 422
    // for the same value is covered by the Go handler test
    // TestSetParameterValidatesPasswordParams.)
    await cost.fill("99");

    // The input reports invalid against its max bound.
    const validity = await cost.evaluate((el: HTMLInputElement) => ({
      valid: el.checkValidity(),
      max: el.max,
    }));
    expect(validity.valid).toBe(false);
    expect(validity.max).toBe("31");

    // Clicking save does not produce a saved confirmation (the submit is blocked).
    await page.getByTestId("hashing-save").click();
    await expect(page.getByTestId("hashing-saved")).toHaveCount(0);
  });

  test("switching to bcrypt cost 12 saves with a confirmation", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");
    await navigateToHashing(page);

    await selectAlgorithm(page, "bcrypt");

    // The bcrypt cost input appears; set a valid cost.
    const cost = page.getByTestId("hashing-bcrypt-cost");
    await expect(cost).toBeVisible();
    await cost.fill("12");

    await page.getByTestId("hashing-save").click();

    await expect(page.getByTestId("hashing-saved")).toBeVisible({ timeout: 10000 });
  });
});
