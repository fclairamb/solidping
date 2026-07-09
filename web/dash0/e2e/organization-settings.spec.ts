import { test, expect } from "./fixtures";

// Covers the org-settings "Session length" card (organization.settings.tsx,
// spec 2026-07-08-01 B.4): a per-org auth.session_max_duration override,
// editable through the real dashboard form and PATCH /api/v1/orgs/:org/settings.
// The backend already has a service-level round trip test
// (TestOrgSettingsSessionMaxDurationRoundTrip); this file is the missing
// end-to-end leg that actually drives the UI form through the real HTTP
// request and confirms the value survives a reload (not just a client-side
// cache hit).
test.describe("Organization settings — session length override", () => {
  // Best-effort cleanup: clear the override afterwards so a stray hard cap
  // left on the shared "test" org doesn't affect other specs/workers that
  // also authenticate against it later in the same run.
  test.afterEach(async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/organization/settings");
    await page.waitForLoadState("networkidle");
    const hoursInput = page.getByTestId("session-duration-hours");
    await hoursInput.fill("");
    await page.getByTestId("session-duration-save").click();
    await expect(page.getByText(/settings saved/i)).toBeVisible({
      timeout: 10000,
    });
  });

  test("setting a session length persists through the real PATCH and survives a reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/organization/settings");
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByRole("heading", { name: /session length/i }),
    ).toBeVisible();

    const hoursInput = page.getByTestId("session-duration-hours");

    // A large value (11h) that no other test in this shared "test" org could
    // plausibly run long enough to hit, so the hard cap it introduces can't
    // bleed into unrelated specs sharing this org/session for the rest of
    // the run.
    await hoursInput.fill("11");

    const patchResponsePromise = page.waitForResponse(
      (resp) =>
        resp.url().includes("/api/v1/orgs/test/settings") &&
        resp.request().method() === "PATCH",
    );
    await page.getByTestId("session-duration-save").click();
    const patchResponse = await patchResponsePromise;

    expect(patchResponse.status()).toBe(200);
    const patchBody = await patchResponse.json();
    expect(patchBody.sessionMaxDurationSeconds).toBe(11 * 3600);

    await expect(page.getByText(/settings saved/i)).toBeVisible();

    // Reload to force a fresh GET /settings — proves the value was actually
    // persisted server-side, not just held in the mutation's local state.
    await page.reload();
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("session-duration-hours")).toHaveValue(
      "11",
    );
  });

  test("clearing the override reverts the org to the platform default", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/organization/settings");
    await page.waitForLoadState("networkidle");

    const hoursInput = page.getByTestId("session-duration-hours");

    // Set a value first so there is something to clear.
    await hoursInput.fill("6");
    const setPatchPromise = page.waitForResponse(
      (resp) =>
        resp.url().includes("/api/v1/orgs/test/settings") &&
        resp.request().method() === "PATCH",
    );
    await page.getByTestId("session-duration-save").click();
    await setPatchPromise;
    await expect(page.getByText(/settings saved/i)).toBeVisible();

    // Now clear it — an empty field means "inherit the platform default"
    // (sessionMaxDurationSeconds <= 0 clears the override server-side).
    await hoursInput.fill("");
    const clearPatchPromise = page.waitForResponse(
      (resp) =>
        resp.url().includes("/api/v1/orgs/test/settings") &&
        resp.request().method() === "PATCH",
    );
    await page.getByTestId("session-duration-save").click();
    const clearPatchResponse = await clearPatchPromise;

    expect(clearPatchResponse.status()).toBe(200);
    const clearBody = await clearPatchResponse.json();
    expect(clearBody.sessionMaxDurationSeconds ?? null).toBeNull();

    await page.reload();
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("session-duration-hours")).toHaveValue("");
  });
});
