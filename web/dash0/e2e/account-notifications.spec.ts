import { test, expect } from "./fixtures";

/**
 * E2E tests for Account → Notifications page.
 *
 * These tests run against a real dev server with the test user
 * (test@test.com / test / org=test). The API auto-seeds an email
 * route on first GET, so the email row always appears.
 */
test.describe("Account Notifications", () => {
  test("should show auto-seeded email row on first visit", async ({
    authenticatedPage: page,
  }) => {
    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    // The email contact should be auto-seeded and visible.
    const list = page.getByTestId("notification-routes-list");
    await expect(list).toBeVisible();

    // At least one row with "Email" label.
    await expect(list.getByText("Email")).toBeVisible();
  });

  test("should navigate to notifications via account tabs", async ({
    authenticatedPage: page,
  }) => {
    await page.goto("orgs/test/account/profile");
    await page.waitForLoadState("networkidle");

    // Click the Notifications tab.
    await page.getByRole("link", { name: "Notifications" }).click();
    await page.waitForLoadState("networkidle");

    await expect(page).toHaveURL(/account\/notifications/);
  });

  test("should add and display a phone contact", async ({
    authenticatedPage: page,
  }) => {
    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    // Click "Add method".
    await page.getByTestId("add-contact-button").click();

    // Switch to phone.
    await page.getByRole("button", { name: "Phone" }).click();

    // Enter phone number.
    const phoneNumber = `+1555${Date.now().toString().slice(-7)}`;
    await page.getByTestId("add-contact-input").fill(phoneNumber);
    await page.getByTestId("add-contact-submit").click();

    // The phone row should appear in the list.
    await page.waitForLoadState("networkidle");
    const list = page.getByTestId("notification-routes-list");
    await expect(list.getByText("Phone (SMS)")).toBeVisible();
  });

  test("should toggle enabled on email route", async ({
    authenticatedPage: page,
  }) => {
    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    // Find the first switch (email route should be first).
    const switches = page.getByRole("switch");
    await expect(switches.first()).toBeVisible();

    // Check initial state (enabled = true).
    const isChecked = await switches.first().isChecked();

    // Toggle it.
    await switches.first().click();
    await page.waitForLoadState("networkidle");

    // State should have flipped.
    const newChecked = await switches.first().isChecked();
    expect(newChecked).toBe(!isChecked);

    // Toggle back to restore state.
    await switches.first().click();
    await page.waitForLoadState("networkidle");
  });
});
