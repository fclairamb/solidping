import { test, expect } from "./fixtures";
import { expandSection } from "./section-helpers";

test.describe("Check labels", () => {
  test.describe.configure({ mode: "serial" });

  async function gotoNewCheck(page: import("@playwright/test").Page) {
    await page.getByTestId("app-sidebar").getByRole("link", { name: "Checks" }).click();
    await page.waitForURL(/\/checks/);
    await page.waitForLoadState("networkidle");
    await page.getByTestId("new-check-button").click();
    await page.waitForURL(/\/checks\/new/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();
    // Labels now live in the collapsed "Organization" section.
    await expandSection(page, "section-organization-trigger");
  }

  async function fillBasics(
    page: import("@playwright/test").Page,
    name: string,
    url: string,
  ) {
    await page.getByTestId("check-name-input").fill(name);
    await page.getByTestId("check-url-input").fill(url);
  }

  async function addLabel(
    page: import("@playwright/test").Page,
    key: string,
    value: string,
  ) {
    const keyInput = page.getByTestId("label-key-input");
    await keyInput.click();
    await keyInput.fill(key);
    await page.getByTestId("label-key-use-typed").click();

    const valueInput = page.getByTestId("label-value-input");
    await valueInput.click();
    await valueInput.fill(value);
    await valueInput.press("Enter");
  }

  test("creates a check with a multi-char label and persists", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await gotoNewCheck(page);

    const timestamp = Date.now();
    const name = `E2E Labels Service ${timestamp}`;
    const url = `https://httpbin.org/anything/labels-${timestamp}`;
    await fillBasics(page, name, url);

    await addLabel(page, "service", "payments");

    await expect(page.getByTestId("label-chips")).toContainText("service: payments");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f-]{36}$/, { timeout: 10000 });

    // Reload via the edit page and assert the chip is preserved. The section
    // auto-opens because labels are set; expand defensively regardless.
    await page.goto(page.url() + "/edit");
    await page.waitForLoadState("networkidle");
    await expandSection(page, "section-organization-trigger");
    await expect(page.getByTestId("label-chips")).toContainText("service: payments");
  });

  test("3-char key (env) is accepted and persists", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await gotoNewCheck(page);

    const timestamp = Date.now();
    const name = `E2E Labels Env ${timestamp}`;
    const url = `https://httpbin.org/anything/env-${timestamp}`;
    await fillBasics(page, name, url);

    const keyInput = page.getByTestId("label-key-input");
    await keyInput.click();
    await keyInput.fill("env");

    // No regex error should be shown for a valid 3-char key.
    await expect(page.getByTestId("label-key-error")).toHaveCount(0);

    await page.getByTestId("label-key-use-typed").click();
    const valueInput = page.getByTestId("label-value-input");
    await valueInput.click();
    await valueInput.fill("prod");
    await valueInput.press("Enter");

    await expect(page.getByTestId("label-chips")).toContainText("env: prod");

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f-]{36}$/, { timeout: 10000 });

    await page.goto(page.url() + "/edit");
    await page.waitForLoadState("networkidle");
    await expandSection(page, "section-organization-trigger");
    await expect(page.getByTestId("label-chips")).toContainText("env: prod");
  });

  test("rejects too-short keys inline", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await gotoNewCheck(page);

    const keyInput = page.getByTestId("label-key-input");
    await keyInput.click();

    await keyInput.fill("e");
    await expect(page.getByTestId("label-key-error")).toBeVisible();

    await keyInput.fill("en");
    await expect(page.getByTestId("label-key-error")).toBeVisible();

    await keyInput.fill("env");
    await expect(page.getByTestId("label-key-error")).toHaveCount(0);
  });

  test("rejects invalid characters in keys", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await gotoNewCheck(page);

    const keyInput = page.getByTestId("label-key-input");
    await keyInput.click();

    // Capitals not allowed.
    await keyInput.fill("Env");
    await expect(page.getByTestId("label-key-error")).toBeVisible();

    // Underscore not allowed (only letters, digits, hyphens).
    await keyInput.fill("env_x");
    await expect(page.getByTestId("label-key-error")).toBeVisible();

    // Leading digit not allowed.
    await keyInput.fill("1env");
    await expect(page.getByTestId("label-key-error")).toBeVisible();
  });

  test("removes a chip via the × button", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await gotoNewCheck(page);

    const timestamp = Date.now();
    await fillBasics(
      page,
      `E2E Labels Remove ${timestamp}`,
      `https://httpbin.org/anything/remove-${timestamp}`,
    );

    await addLabel(page, "team", "ops");
    await expect(page.getByTestId("label-chips")).toContainText("team: ops");

    await page.getByTestId("label-chip-remove-team").click();
    await expect(page.getByTestId("label-chips")).toHaveCount(0);
  });
});
