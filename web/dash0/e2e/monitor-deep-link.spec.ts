import { test, expect } from "./fixtures";

// Spec 2026-10-14-03: the org-less /d/monitor deep link published by the
// website's free tools lands on a prefilled new-check form, through login
// when the visitor is signed out. The prefill only fills the form.
const QUERY = "checkType=http&httpUrl=https://example.com";

test.describe("Monitor deep link", () => {
  test("signed out: login, then the prefilled new-check form", async ({
    page,
  }) => {
    await page.goto(`monitor?${QUERY}&evil=1`);

    await expect(page.getByTestId("login-title")).toBeVisible({
      timeout: 15000,
    });
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();

    // The test user belongs to several orgs: pick one, as a real user would.
    await page.getByTestId("org-picker-test").click();

    await expect(page.getByTestId("check-url-input")).toHaveValue(
      "https://example.com",
      { timeout: 15000 },
    );
    expect(page.url()).toContain("/orgs/test/checks/new");
    // The allowlist drops unknown keys.
    expect(page.url()).not.toContain("evil");
    // Consumed by the /monitor visit: nothing is left to fire on `/`.
    expect(
      await page.evaluate(() =>
        localStorage.getItem("solidping_pending_check"),
      ),
    ).toBeNull();
  });

  test("signed in: lands on the prefilled form directly", async ({
    authenticatedPage: page,
  }) => {
    await page.goto(`monitor?${QUERY}`);

    await expect(page.getByTestId("check-url-input")).toHaveValue(
      "https://example.com",
      { timeout: 15000 },
    );
    expect(page.url()).toContain("/orgs/test/checks/new");
  });
});
