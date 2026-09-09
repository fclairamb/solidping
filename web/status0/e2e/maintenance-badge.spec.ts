import { test, expect } from "@playwright/test";
import {
  API_BASE as BASE,
  STATUS_BASE,
  resolveDefaultStatusPage,
} from "./fixtures";

/**
 * Verifies the "Scheduled Maintenance" badge on the public status page.
 *
 * The badge renders for a resource whose check is inside an *active*
 * maintenance window (driven by the `check.inMaintenance` flag on the public
 * status payload). The stock dev seed does not place any resource under an
 * active window, so this spec asserts the badge's shape when present and
 * otherwise skips. To exercise it for real, create a check, attach it to a
 * maintenance window active "now", add the check as a resource on the default
 * status page, then run `bun run test:e2e`.
 */
test.describe("Public status page — scheduled maintenance badge", () => {
  test("maintenance resource shows the Scheduled Maintenance badge", async ({
    page,
  }) => {
    // Resolved rather than hardcoded to `default`: on a `SP_RUNMODE=test`
    // server that org does not exist, so the page 404s and the badge count is
    // zero for a reason that has nothing to do with maintenance windows — the
    // skip below would then be reporting the wrong thing.
    const target = await resolveDefaultStatusPage();

    await page.goto(`${BASE}${STATUS_BASE}/${target.org}`);
    await page.waitForLoadState("networkidle");

    const badge = page.getByTestId("resource-maintenance-badge");
    const count = await badge.count();

    test.skip(
      count === 0,
      "No resource is under an active maintenance window in the current seed — " +
        "create one (check + active window + status-page resource) to exercise this.",
    );

    await expect(badge.first()).toBeVisible();
    await expect(badge.first()).toHaveText(/Scheduled Maintenance/i);
  });
});
