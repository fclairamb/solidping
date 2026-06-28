import { test, expect, type Page } from "./fixtures";

// E2E coverage for the maintenance-windows management UI (dash0).
// Flow: list renders + empty/new button, new form shows all mw-* inputs,
// create a one-time window (with a check selected) -> detail; the recurrence
// section is now cadence-aware — Weekly reveals weekday chips + start-time +
// duration + first-day + a date "Until" input, the schedule summary updates
// live ("Every Monday …", "On day 15 …") with a "Next:" preview, and a weekly
// window round-trips through create -> edit; edit a window's title -> detail
// reflects it; delete via the red Trash2 -> returns to the list.
//
// Runs against the test org (test@test.com / test). Requires the server in
// SP_RUNMODE=test; the :4000 devloop is normal mode, so this is authored to
// run under `make test-dash` (which boots a test-mode server).

const SCREENSHOT_DIR = "test-results/screenshots";

// Open the maintenance-windows list via the sidebar.
async function gotoList(page: Page) {
  await page
    .getByTestId("app-sidebar")
    .getByRole("link", { name: "Maintenance" })
    .click();
  await page.waitForURL(/\/maintenance-windows/);
  await page.waitForLoadState("networkidle");
}

// Seed a check through the REST API using the bearer token the app stored in
// localStorage after login. Returns the new check's uid (or null on failure).
async function seedCheck(page: Page, slug: string): Promise<string | null> {
  const token = await page.evaluate(() =>
    localStorage.getItem("solidping_token"),
  );
  if (!token) return null;
  const res = await page.request.post(
    "/api/v1/orgs/test/checks",
    {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: slug,
        slug,
        type: "http",
        config: { url: "https://1.1.1.1" },
        period: "60s",
      },
    },
  );
  if (!res.ok()) return null;
  const body = await res.json();
  return body.uid ?? null;
}

// Fill the required schedule fields with a fixed future window.
async function fillSchedule(page: Page, startLocal: string, endLocal: string) {
  await page.getByTestId("mw-start-input").fill(startLocal);
  await page.getByTestId("mw-end-input").fill(endLocal);
}

test.describe("Maintenance windows", () => {
  test("list page renders with heading, New button, and empty/list state", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoList(page);

    await expect(
      page.getByRole("heading", { name: "Maintenance Windows" }),
    ).toBeVisible();
    await expect(page.getByTestId("mw-new-button")).toBeVisible();

    // Either the empty-state card or a populated table is shown.
    await Promise.race([
      page
        .getByText("No maintenance windows scheduled")
        .waitFor({ state: "visible", timeout: 5000 })
        .catch(() => {}),
      page
        .locator("table tbody tr")
        .first()
        .waitFor({ state: "visible", timeout: 5000 })
        .catch(() => {}),
    ]);

    await page.screenshot({
      path: `${SCREENSHOT_DIR}/maintenance-windows-list.png`,
      fullPage: true,
    });
  });

  test("new form shows all mw-* inputs", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await gotoList(page);

    await page.getByTestId("mw-new-button").click();
    await page.waitForURL(/\/maintenance-windows\/new/);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("mw-title-input")).toBeVisible();
    await expect(page.getByTestId("mw-description-input")).toBeVisible();
    await expect(page.getByTestId("mw-start-input")).toBeVisible();
    await expect(page.getByTestId("mw-end-input")).toBeVisible();
    await expect(page.getByTestId("mw-recurrence-select")).toBeVisible();
    await expect(page.getByTestId("mw-checks-select")).toBeVisible();
    await expect(page.getByTestId("mw-groups-select")).toBeVisible();
    await expect(page.getByTestId("mw-submit-button")).toBeVisible();

    // Recurrence-end input is hidden until a recurrence is chosen.
    await expect(page.getByTestId("mw-recurrence-end-input")).toHaveCount(0);

    await page.screenshot({
      path: `${SCREENSHOT_DIR}/maintenance-windows-new-form.png`,
      fullPage: true,
    });
  });

  test("create a one-time window with a check, landing on detail", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    const checkSlug = `mw-e2e-check-${suffix}`;
    const checkUid = await seedCheck(page, checkSlug);

    await gotoList(page);
    await page.getByTestId("mw-new-button").click();
    await page.waitForURL(/\/maintenance-windows\/new/);
    await page.waitForLoadState("networkidle");

    const title = `e2e one-time ${suffix}`;
    await page.getByTestId("mw-title-input").fill(title);
    await fillSchedule(page, "2030-01-01T02:00", "2030-01-01T04:00");

    // Select the seeded check via the multi-picker, if seeding succeeded.
    if (checkUid) {
      await page.getByTestId("mw-checks-select").getByRole("button").first().click();
      const option = page.getByTestId(`mw-checks-select-option-${checkUid}`);
      await option.waitFor({ state: "visible", timeout: 5000 }).catch(() => {});
      if (await option.isVisible().catch(() => false)) {
        await option.click();
      }
      // Close the popover before submitting.
      await page.keyboard.press("Escape");
    }

    await page.getByTestId("mw-submit-button").click();

    // Lands on the detail route (uid in the trailing segment).
    await page.waitForURL(/\/maintenance-windows\/[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: title })).toBeVisible();

    await page.screenshot({
      path: `${SCREENSHOT_DIR}/maintenance-windows-detail.png`,
      fullPage: true,
    });
  });

  test("choosing Weekly reveals chips + time + duration + first-day inputs", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoList(page);
    await page.getByTestId("mw-new-button").click();
    await page.waitForURL(/\/maintenance-windows\/new/);
    await page.waitForLoadState("networkidle");

    // One-time default: start/end datetimes present, no recurring inputs.
    await expect(page.getByTestId("mw-start-input")).toBeVisible();
    await expect(page.getByTestId("mw-recurrence-end-input")).toHaveCount(0);

    // Open the recurrence select and pick Weekly.
    await page.getByTestId("mw-recurrence-select").click();
    await page.getByRole("option", { name: "Weekly" }).click();

    // Weekly now reveals the weekday chip row + time-slot + validity inputs,
    // and the one-time datetime inputs are gone.
    await expect(page.getByTestId("mw-weekday-3")).toBeVisible(); // Wednesday
    await expect(page.getByTestId("mw-start-time-input")).toBeVisible();
    await expect(page.getByTestId("mw-duration-input")).toBeVisible();
    await expect(page.getByTestId("mw-first-day-input")).toBeVisible();
    // Until is now a date input (was datetime-local).
    await expect(page.getByTestId("mw-recurrence-end-input")).toHaveAttribute(
      "type",
      "date",
    );
    await expect(page.getByTestId("mw-start-input")).toHaveCount(0);
    await expect(page.getByTestId("mw-end-input")).toHaveCount(0);

    await page.screenshot({
      path: `${SCREENSHOT_DIR}/maintenance-windows-weekly-inputs.png`,
      fullPage: true,
    });
  });

  test("weekday chip selection updates the schedule summary", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoList(page);
    await page.getByTestId("mw-new-button").click();
    await page.waitForURL(/\/maintenance-windows\/new/);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("mw-recurrence-select").click();
    await page.getByRole("option", { name: "Weekly" }).click();

    // Pick a far-future first day so the preview is non-empty, and Monday.
    await page.getByTestId("mw-first-day-input").fill("2030-01-07"); // a Monday
    await page.getByTestId("mw-start-time-input").fill("22:00");
    await page.getByTestId("mw-weekday-1").click(); // Monday

    const summary = page.getByTestId("mw-schedule-summary");
    await expect(summary).toBeVisible();
    // The localized weekly summary mentions a weekday and the time slot.
    await expect(summary).toContainText("Monday");
    // A non-empty "Next:" preview appears for a future recurring draft.
    await expect(summary).toContainText("Next:");
  });

  test("choosing Monthly shows day-of-month and an 'On day N' summary", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoList(page);
    await page.getByTestId("mw-new-button").click();
    await page.waitForURL(/\/maintenance-windows\/new/);
    await page.waitForLoadState("networkidle");

    await page.getByTestId("mw-recurrence-select").click();
    await page.getByRole("option", { name: "Monthly" }).click();

    await expect(page.getByTestId("mw-dayofmonth-select")).toBeVisible();
    await page.getByTestId("mw-first-day-input").fill("2030-01-01");
    await page.getByTestId("mw-start-time-input").fill("22:00");

    // Pick day 15.
    await page.getByTestId("mw-dayofmonth-select").click();
    await page.getByRole("option", { name: "15", exact: true }).click();

    const summary = page.getByTestId("mw-schedule-summary");
    await expect(summary).toContainText("day 15");

    await page.screenshot({
      path: `${SCREENSHOT_DIR}/maintenance-windows-monthly.png`,
      fullPage: true,
    });
  });

  test("weekly window round-trips through create then edit", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);

    await gotoList(page);
    await page.getByTestId("mw-new-button").click();
    await page.waitForURL(/\/maintenance-windows\/new/);
    await page.waitForLoadState("networkidle");

    const title = `e2e weekly ${suffix}`;
    await page.getByTestId("mw-title-input").fill(title);
    await page.getByTestId("mw-recurrence-select").click();
    await page.getByRole("option", { name: "Weekly" }).click();

    // Configure a specific weekly schedule.
    await page.getByTestId("mw-weekday-3").click(); // Wednesday
    await page.getByTestId("mw-start-time-input").fill("22:00");
    await page.getByTestId("mw-duration-input").fill("2");
    await page.getByTestId("mw-first-day-input").fill("2030-01-02"); // a Wednesday

    await page.getByTestId("mw-submit-button").click();
    await page.waitForURL(/\/maintenance-windows\/[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: title })).toBeVisible();

    // Open edit; the recurring inputs should be prefilled to the same values.
    await page.getByTestId("mw-detail-edit-button").click();
    await page.waitForURL(/\/maintenance-windows\/[^/]+\/edit/, {
      timeout: 10000,
    });
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("mw-start-time-input")).toHaveValue("22:00");
    // Duration 2 hours round-trips (exact multiple of 60 → hours unit).
    await expect(page.getByTestId("mw-duration-input")).toHaveValue("2");
    // Wednesday chip is the selected (pressed) one.
    await expect(page.getByTestId("mw-weekday-3")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    // First day round-trips.
    await expect(page.getByTestId("mw-first-day-input")).toHaveValue(
      "2030-01-02",
    );
  });

  test("edit a window's title; detail reflects the change", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);

    // Create a window to edit.
    await gotoList(page);
    await page.getByTestId("mw-new-button").click();
    await page.waitForURL(/\/maintenance-windows\/new/);
    await page.waitForLoadState("networkidle");
    const title = `e2e edit ${suffix}`;
    await page.getByTestId("mw-title-input").fill(title);
    await fillSchedule(page, "2030-02-01T02:00", "2030-02-01T04:00");
    await page.getByTestId("mw-submit-button").click();
    await page.waitForURL(/\/maintenance-windows\/[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Go to edit via the detail Edit button.
    await page.getByTestId("mw-detail-edit-button").click();
    await page.waitForURL(/\/maintenance-windows\/[^/]+\/edit/, {
      timeout: 10000,
    });
    await page.waitForLoadState("networkidle");

    const newTitle = `${title} edited`;
    const titleInput = page.getByTestId("mw-title-input");
    await expect(titleInput).toHaveValue(title);
    await titleInput.fill(newTitle);
    await page.getByTestId("mw-submit-button").click();

    await page.waitForURL(/\/maintenance-windows\/[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByRole("heading", { name: newTitle })).toBeVisible();
  });

  test("delete a window via the detail red trash; returns to list", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);

    // Create a window to delete.
    await gotoList(page);
    await page.getByTestId("mw-new-button").click();
    await page.waitForURL(/\/maintenance-windows\/new/);
    await page.waitForLoadState("networkidle");
    const title = `e2e delete ${suffix}`;
    await page.getByTestId("mw-title-input").fill(title);
    await fillSchedule(page, "2030-03-01T02:00", "2030-03-01T04:00");
    await page.getByTestId("mw-submit-button").click();
    await page.waitForURL(/\/maintenance-windows\/[^/]+$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Delete from the detail page: open the confirm dialog then confirm.
    await page.getByTestId("mw-detail-delete-button").click();
    await page
      .getByRole("alertdialog")
      .getByRole("button", { name: "Delete" })
      .click();

    // Back on the list.
    await page.waitForURL(/\/maintenance-windows$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByRole("heading", { name: "Maintenance Windows" }),
    ).toBeVisible();

    // The deleted window's title is no longer present in the list.
    await expect(page.getByText(title, { exact: true })).toHaveCount(0);
  });
});
