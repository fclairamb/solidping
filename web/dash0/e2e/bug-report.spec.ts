import { test, expect } from "./fixtures";

// In-app bug reports — covers the four scenarios from the spec:
// desktop happy path, keyboard shortcut, mobile layout via dispatched
// event, and feature flag off (icon hidden, shortcut no-op).

test.describe("Bug report", () => {
  test("desktop happy path: opens dialog, captures, submits payload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Force features.bugReport=true so the icon renders even when the
    // backend hasn't been configured with a GitHub token.
    await page.route("**/api/v1/features", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ bugReport: true }),
      }),
    );

    // Stub the report endpoint so the test passes regardless of GitHub
    // connectivity.
    let submittedFormFields: string[] = [];
    await page.route("**/api/mgmt/report", async (route) => {
      const body = route.request().postData() || "";
      submittedFormFields = body.split("\r\n").filter(Boolean);
      await route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
    });

    await page.reload();
    await page.waitForLoadState("networkidle");

    await page.getByTestId("feedback-button").click();

    const comment = page.getByTestId("feedback-comment");
    await comment.waitFor({ state: "visible" });
    await comment.fill("Found a bug on the dashboard");

    await page.getByTestId("feedback-submit").click();

    await expect.poll(() => submittedFormFields.length).toBeGreaterThan(0);
    expect(submittedFormFields.join("\n")).toContain(
      "Found a bug on the dashboard",
    );
  });

  test("keyboard shortcut opens the dialog", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.route("**/api/v1/features", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ bugReport: true }),
      }),
    );
    await page.reload();
    await page.waitForLoadState("networkidle");

    const isMac = await page.evaluate(() => /Mac/.test(navigator.platform));
    const modifier = isMac ? "Meta" : "Control";
    await page.keyboard.press(`${modifier}+Shift+B`);

    await expect(page.getByTestId("feedback-comment")).toBeVisible();
  });

  test("mobile layout (event-triggered): textarea visible, no annotation toolbar", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 667 });

    await page.route("**/api/v1/features", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ bugReport: true }),
      }),
    );
    await page.reload();
    await page.waitForLoadState("networkidle");

    await page.evaluate(() => {
      window.dispatchEvent(new Event("feedback:open"));
    });

    await expect(page.getByTestId("feedback-comment")).toBeVisible();
    // Annotation toolbar/canvas hidden via `hidden sm:block` on small viewports.
    await expect(page.getByTestId("annotation-canvas")).toBeHidden();
  });

  test("feature off: bug icon is not rendered and shortcut is a no-op", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.route("**/api/v1/features", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ bugReport: false }),
      }),
    );
    await page.reload();
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("feedback-button")).toBeHidden();

    const isMac = await page.evaluate(() => /Mac/.test(navigator.platform));
    const modifier = isMac ? "Meta" : "Control";
    await page.keyboard.press(`${modifier}+Shift+B`);

    await page.waitForTimeout(200);
    await expect(page.getByTestId("feedback-comment")).toBeHidden();
  });
});
