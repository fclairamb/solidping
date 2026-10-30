import { test, expect } from "./fixtures";

// The sign-up and password-reset forms send back the form ticket they fetch on
// render, plus an empty honeypot field (auth.form_guard), and the
// confirmation link spends its one-shot token only on a click. The POSTs are
// answered here, so nothing is created and no email is queued.

test.describe("Form guard", () => {
  test("register sends a form ticket and an empty honeypot the visitor cannot reach", async ({
    page,
  }) => {
    let sent: Record<string, unknown> | null = null;
    await page.route("**/api/v1/auth/register", async (route) => {
      sent = route.request().postDataJSON() as Record<string, unknown>;
      await route.fulfill({ status: 200, json: { message: "ok" } });
    });

    await page.goto("orgs/test/register");

    // Out of the tab order and hidden from assistive technology.
    const honeypot = page.getByTestId("form-honeypot-input");
    await expect(honeypot).toHaveAttribute("tabindex", "-1");
    await expect(honeypot).toHaveAttribute("autocomplete", "off");
    await expect(page.getByTestId("form-honeypot")).toHaveAttribute("aria-hidden", "true");

    await page.getByTestId("register-email").fill("alice@acme.com");
    await page.getByTestId("register-password").fill("correct-horse-battery");
    await page.getByTestId("register-submit").click();

    await expect.poll(() => sent).not.toBeNull();
    const body = sent as unknown as Record<string, unknown>;
    expect(typeof body.formTicket).toBe("string");
    expect((body.formTicket as string).length).toBeGreaterThan(20);
    expect(body.website).toBeUndefined();
    await expect(page.getByTestId("register-confirmation-message")).toBeVisible();
  });

  test("forgot password sends a form ticket", async ({ page }) => {
    let sent: Record<string, unknown> | null = null;
    await page.route("**/api/v1/auth/request-password-reset", async (route) => {
      sent = route.request().postDataJSON() as Record<string, unknown>;
      await route.fulfill({ status: 200, json: { message: "ok" } });
    });

    await page.goto("forgot-password?email=alice%40acme.com");
    await page.getByTestId("forgot-password-submit").click();

    // Submitted at once with the pre-filled email: the page waits out the
    // ticket's minimum age instead of being dropped, then shows success.
    await expect(page.getByTestId("forgot-password-sent")).toBeVisible({ timeout: 10000 });
    const body = sent as unknown as Record<string, unknown>;
    expect(body.email).toBe("alice@acme.com");
    expect(typeof body.formTicket).toBe("string");
    expect(body.website).toBeUndefined();
  });

  test("the confirmation link spends its token only on a click", async ({ page }) => {
    let calls = 0;
    await page.route("**/api/v1/auth/confirm-registration", async (route) => {
      calls += 1;
      await route.fulfill({
        status: 410,
        json: { title: "Registration link has expired or is invalid", code: "REGISTRATION_EXPIRED" },
      });
    });

    await page.goto("confirm-registration/e2e-form-guard-token");
    await expect(page.getByTestId("confirm-registration-button")).toBeVisible();
    await page.waitForLoadState("networkidle");
    expect(calls).toBe(0);

    // Positive control: the click does reach the endpoint, once.
    await page.getByTestId("confirm-registration-button").click();
    await expect(page.getByTestId("confirm-registration-error")).toBeVisible();
    expect(calls).toBe(1);
  });
});
