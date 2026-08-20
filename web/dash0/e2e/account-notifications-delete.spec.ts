import { test, expect } from "./fixtures";

/**
 * E2E coverage for deleting a notification method (spec 2026-08-20-02).
 *
 * The bug this pins: deleting a contact soft-deleted the contact but left its
 * `user_notification_routes` row behind, and the list endpoint kept returning
 * that route with an all-empty contact (`uid=""`, `type=""`, `value=""`). The
 * page rendered it as an undeletable ghost row — the generic BellRing fallback
 * icon, no title, no value — and its delete button called the API with an empty
 * contact uid, which could never match anything ("Failed to remove notification
 * contact" forever).
 *
 * A ghost row is therefore exactly a row whose `route.contact.uid` is `""`,
 * which the page stamps as `data-testid="delete-contact-"` (the interpolated
 * uid is empty). Asserting that locator has zero matches is a direct, precise
 * check for the defect — much stronger than eyeballing a row count, which a
 * ghost row would satisfy just as well as a real one.
 *
 * Runs against a real dev server with the test user (test@test.com / test /
 * org=test) — no route mocking, because the whole defect lives in what the
 * backend returns after a real DELETE.
 */
test.describe("Account Notifications — deleting a method", () => {
  test("removes the row and leaves no ghost behind", async ({
    authenticatedPage: page,
  }) => {
    await page.goto("orgs/test/account/notifications");
    await page.waitForLoadState("networkidle");

    const list = page.getByTestId("notification-routes-list");
    await expect(list).toBeVisible();

    // Every row carries a delete button keyed on its contact uid; counting
    // them counts rows.
    const rows = list.locator('[data-testid^="delete-contact-"]');
    const ghosts = page.locator('[data-testid="delete-contact-"]');

    // Baseline: whatever this org already has, none of it is a ghost.
    await expect(ghosts).toHaveCount(0);
    const before = await rows.count();

    // Add a phone method so this test owns the row it deletes (the suite runs
    // against a shared org, and the auto-seeded email must not be touched —
    // other specs in this file depend on it).
    const phoneNumber = `+1555${Date.now().toString().slice(-7)}`;
    await page.getByTestId("add-contact-button").click();
    await page.getByTestId("add-contact-type-phone").click();
    await page.getByTestId("add-contact-input").fill(phoneNumber);
    await page.getByTestId("add-contact-submit").click();

    await expect(list.getByText(phoneNumber)).toBeVisible();
    await expect(rows).toHaveCount(before + 1);

    // Delete it through its OWN row's trash button — found by the number we
    // just typed, not by index, so this can never delete a sibling row if the
    // ordering ever changes.
    const phoneRow = list.locator("> div").filter({ hasText: phoneNumber });
    await phoneRow.locator('[data-testid^="delete-contact-"]').click();

    // The row count drops back...
    await expect(rows).toHaveCount(before);
    // ...the deleted method is really gone, not re-rendered blank...
    await expect(list.getByText(phoneNumber)).toHaveCount(0);
    // ...and crucially, no row rendered with an empty contact uid. Before the
    // fix this is where the ghost appeared: the count would stay at
    // `before + 1` with one unlabelled, undeletable row.
    await expect(ghosts).toHaveCount(0);

    // A reload proves it is gone server-side, not just optimistically removed
    // from the React Query cache — the ghost was a persisted DB row, so a
    // cache-only assertion would miss it entirely.
    await page.reload();
    await page.waitForLoadState("networkidle");
    await expect(ghosts).toHaveCount(0);
    await expect(page.getByText(phoneNumber)).toHaveCount(0);
  });
});
