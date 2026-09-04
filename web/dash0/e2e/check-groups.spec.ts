import { test, expect, API_BASE, type Page } from "./fixtures";
import { expandSection } from "./section-helpers";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function createGroupViaApi(
  page: Page,
  token: string,
  name: string
): Promise<{ uid: string; name: string; slug: string }> {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/check-groups`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { name },
    }
  );
  expect(resp.status()).toBe(201);
  return resp.json();
}

async function createCheckViaApi(
  page: Page,
  token: string,
  name: string,
  checkGroupUid?: string
): Promise<{ uid: string }> {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/checks`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        type: "http",
        name,
        config: { url: `https://example.com/${Date.now()}` },
        period: "00:05:00",
        ...(checkGroupUid ? { checkGroupUid } : {}),
      },
    }
  );
  expect(resp.status()).toBe(201);
  return resp.json();
}

async function deleteGroupViaApi(
  page: Page,
  token: string,
  uid: string
): Promise<void> {
  await page.request.delete(
    `${API_BASE}/api/v1/orgs/test/check-groups/${uid}`,
    {
      headers: { Authorization: `Bearer ${token}` },
    }
  );
}

async function navigateToChecks(page: Page) {
  // Force a fresh load of the checks page to pick up any API-created data
  const checksUrl = page.url().replace(/\/orgs\/([^/]+).*/, "/orgs/$1/checks");
  await page.goto(checksUrl);
  await page.waitForLoadState("networkidle");
}

test.describe("Check Groups", () => {
  test("should create a new check group", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await navigateToChecks(page);

    // Click "New Group" button
    await page.getByTestId("new-group-button").click();

    // Wait for dialog to appear
    const dialog = page.getByRole("dialog", { name: "New Group" });
    await expect(dialog).toBeVisible();

    // Fill in the group name
    const groupName = `E2E Group ${Date.now()}`;
    await page.getByTestId("new-group-name-input").fill(groupName);

    // Submit
    await page.getByTestId("new-group-submit").click();

    // Wait for dialog to close
    await expect(dialog).not.toBeVisible({ timeout: 10000 });

    // Wait for the group to appear in the page
    await expect(
      page.getByTestId("group-name").getByText(groupName)
    ).toBeVisible({ timeout: 10000 });

    // Take screenshot
    await page.screenshot({
      path: "test-results/screenshots/check-groups-created.png",
      fullPage: true,
    });
  });

  test("should create a check inside a group", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create group and check via API, then verify they appear correctly
    const token = await getAuthToken(page);
    const ts = Date.now();
    const groupName = `E2E WithCheck ${ts}`;
    const group = await createGroupViaApi(page, token, groupName);
    const checkName = `E2E Grouped Check ${ts}`;
    await createCheckViaApi(page, token, checkName, group.uid);

    await navigateToChecks(page);

    // Verify the group section exists and contains the check
    const groupSection = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(groupName) });
    await expect(groupSection).toBeVisible({ timeout: 10000 });
    await expect(groupSection.getByText(checkName)).toBeVisible({
      timeout: 10000,
    });

    // Clean up
    await deleteGroupViaApi(page, token, group.uid);
  });

  test("should rename a check group", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Create a group via API
    const token = await getAuthToken(page);
    const originalName = `E2E Rename ${Date.now()}`;
    const group = await createGroupViaApi(page, token, originalName);

    await navigateToChecks(page);

    // Find the group section and open its edit page
    const groupSection = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(originalName) });
    await expect(groupSection).toBeVisible({ timeout: 10000 });

    await groupSection.getByTestId("group-edit-button").click();
    await page.waitForURL(/\/check-groups\/[0-9a-f-]+\/edit$/, {
      timeout: 10000,
    });

    // Change the name and save
    const newName = `E2E Renamed ${Date.now()}`;
    const nameInput = page.getByTestId("group-edit-name-input");
    await expect(nameInput).toHaveValue(originalName, { timeout: 10000 });
    await nameInput.clear();
    await nameInput.fill(newName);
    await page.getByTestId("group-edit-submit").click();

    // Saving navigates back to the checks list
    await page.waitForURL(/\/checks$/, { timeout: 10000 });

    // Verify the name has changed
    await expect(
      page.getByTestId("group-name").getByText(newName)
    ).toBeVisible({ timeout: 10000 });

    // Verify old name is gone
    await expect(
      page.getByTestId("group-name").getByText(originalName)
    ).not.toBeVisible();

    // Take screenshot
    await page.screenshot({
      path: "test-results/screenshots/check-groups-renamed.png",
      fullPage: true,
    });

    // Clean up
    await deleteGroupViaApi(page, token, group.uid);
  });

  test("should delete a check group", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Create a group via API
    const token = await getAuthToken(page);
    const groupName = `E2E Delete Group ${Date.now()}`;
    await createGroupViaApi(page, token, groupName);

    await navigateToChecks(page);

    // Find the group section and open its edit page
    const groupSection = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(groupName) });
    await expect(groupSection).toBeVisible({ timeout: 10000 });

    await groupSection.getByTestId("group-edit-button").click();
    await page.waitForURL(/\/check-groups\/[0-9a-f-]+\/edit$/, {
      timeout: 10000,
    });

    // Delete from the edit page, confirming the dialog
    await page.getByTestId("group-delete-button").click();
    await page.getByTestId("confirm-delete-group").click();

    // Deleting navigates back to the checks list; the group is gone
    await page.waitForURL(/\/checks$/, { timeout: 10000 });
    await expect(
      page.getByTestId("group-name").getByText(groupName)
    ).not.toBeVisible({ timeout: 10000 });

    // Take screenshot
    await page.screenshot({
      path: "test-results/screenshots/check-groups-deleted.png",
      fullPage: true,
    });
  });

  test("should reorder check groups", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Create two groups via API
    const token = await getAuthToken(page);
    const ts = Date.now();
    const group1 = await createGroupViaApi(page, token, `E2E First ${ts}`);
    const group2 = await createGroupViaApi(page, token, `E2E Second ${ts}`);

    await navigateToChecks(page);

    // Wait for both groups to be visible
    await expect(
      page.getByTestId("group-name").getByText(`E2E First ${ts}`)
    ).toBeVisible({ timeout: 10000 });
    await expect(
      page.getByTestId("group-name").getByText(`E2E Second ${ts}`)
    ).toBeVisible({ timeout: 10000 });

    // Verify initial order: First should be before Second
    const groupNames = page.getByTestId("group-name");
    const allNames = await groupNames.allTextContents();
    const firstIdx = allNames.findIndex((n) => n === `E2E First ${ts}`);
    const secondIdx = allNames.findIndex((n) => n === `E2E Second ${ts}`);
    expect(firstIdx).toBeLessThan(secondIdx);

    // Move the second group up via its header button
    const secondGroupSection = page
      .getByTestId("group-section")
      .filter({
        has: page.getByTestId("group-name").getByText(`E2E Second ${ts}`),
      });
    await secondGroupSection.getByTestId("group-move-up-button").click();

    // Wait for reorder to take effect
    await page.waitForLoadState("networkidle");
    await page.waitForTimeout(1000);

    // Verify new order: Second should now be before First
    const updatedNames = await groupNames.allTextContents();
    const newFirstIdx = updatedNames.findIndex((n) => n === `E2E First ${ts}`);
    const newSecondIdx = updatedNames.findIndex(
      (n) => n === `E2E Second ${ts}`
    );
    expect(newSecondIdx).toBeLessThan(newFirstIdx);

    // Take screenshot
    await page.screenshot({
      path: "test-results/screenshots/check-groups-reordered.png",
      fullPage: true,
    });

    // Clean up
    await deleteGroupViaApi(page, token, group1.uid);
    await deleteGroupViaApi(page, token, group2.uid);
  });

  test("should create a check with a group via the form", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a group via API
    const token = await getAuthToken(page);
    const ts = Date.now();
    const groupName = `E2E FormGroup ${ts}`;
    const group = await createGroupViaApi(page, token, groupName);

    // Navigate directly to new check form (fresh load to pick up API-created group)
    const newCheckUrl = page.url().replace(/\/orgs\/([^/]+).*/, "/orgs/$1/checks/new");
    await page.goto(newCheckUrl);
    await page.waitForLoadState("networkidle");

    // Wait for form to be ready
    await expect(page.getByTestId("check-name-input")).toBeVisible({
      timeout: 10000,
    });

    // Fill in the check form
    const checkName = `E2E Formed Check ${ts}`;
    await page.getByTestId("check-name-input").fill(checkName);
    await page
      .getByTestId("check-url-input")
      .fill(`https://example.com/${ts}`);

    // Select the group
    await expandSection(page, "section-organization-trigger");
    const groupSelect = page.getByTestId("check-group-select");
    await expect(groupSelect).toBeVisible({ timeout: 10000 });
    await groupSelect.click();
    const groupOption = page.getByRole("option", { name: groupName });
    await expect(groupOption).toBeVisible({ timeout: 10000 });
    await groupOption.click();

    // Wait for selection to be reflected
    await expect(groupSelect).toContainText(groupName, { timeout: 5000 });

    // Submit
    await page.getByTestId("check-submit-button").click();

    // Wait for check detail page
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(
      page.getByRole("heading", { name: checkName })
    ).toBeVisible();

    // Extract check UID from URL and verify group assignment via API
    const checkUid = page.url().match(/checks\/([0-9a-f-]+)/)?.[1];
    expect(checkUid).toBeTruthy();
    const checkResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/checks/${checkUid}`,
      { headers: { Authorization: `Bearer ${token}` } }
    );
    const checkData = await checkResp.json();
    expect(checkData.checkGroupUid).toBe(group.uid);

    // Clean up
    await deleteGroupViaApi(page, token, group.uid);
  });

  test("should add a check to a group from the group header, once the group is non-empty", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a group with an existing check via API — the empty-state link
    // (group-empty-new-check-link) only renders for an empty group, so this
    // exercises the persistent header button instead.
    const token = await getAuthToken(page);
    const ts = Date.now();
    const groupName = `E2E HeaderAdd ${ts}`;
    const group = await createGroupViaApi(page, token, groupName);
    await createCheckViaApi(page, token, `E2E HeaderAdd Existing ${ts}`, group.uid);

    await navigateToChecks(page);

    const groupSection = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(groupName) });
    await expect(groupSection).toBeVisible({ timeout: 10000 });

    // Negative control: the header row (which toggles collapse on click) must
    // stay unaffected by the button click.
    const groupHeader = groupSection.getByTestId("group-header");
    const expandedBefore = await groupHeader.getAttribute("aria-expanded");

    await groupSection.getByTestId("group-add-check-button").click();

    await page.waitForURL(/\/checks\/new\?/, { timeout: 10000 });
    const url = new URL(page.url());
    expect(url.pathname.endsWith("/checks/new")).toBe(true);
    expect(url.searchParams.get("group")).toBe(group.slug);

    // The new-check form has the group preselected.
    await expect(page.getByTestId("check-name-input")).toBeVisible({
      timeout: 10000,
    });
    await expandSection(page, "section-organization-trigger");
    const groupSelect = page.getByTestId("check-group-select");
    await expect(groupSelect).toContainText(groupName, { timeout: 10000 });

    // Verify the negative control: aria-expanded must be unchanged (the
    // header row's collapse state was not touched by the button click). Go
    // back to the checks list and re-check.
    await navigateToChecks(page);
    const groupSectionAfter = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(groupName) });
    await expect(groupSectionAfter).toBeVisible({ timeout: 10000 });
    const expandedAfter = await groupSectionAfter
      .getByTestId("group-header")
      .getAttribute("aria-expanded");
    expect(expandedAfter).toBe(expandedBefore);

    // Clean up
    await deleteGroupViaApi(page, token, group.uid);
  });

  test("should edit a check to assign it to a group", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a group and an ungrouped check via API
    const token = await getAuthToken(page);
    const ts = Date.now();
    const groupName = `E2E AssignGroup ${ts}`;
    const group = await createGroupViaApi(page, token, groupName);
    const checkName = `E2E Assign Check ${ts}`;
    const check = await createCheckViaApi(page, token, checkName);

    // Navigate to the check detail page
    await page.goto(
      `/dash0/orgs/test/checks/${check.uid}`,
      { waitUntil: "networkidle" }
    );
    await expect(
      page.getByRole("heading", { name: checkName })
    ).toBeVisible({ timeout: 10000 });

    // Click Edit
    await page
      .getByTestId("check-detail-header")
      .getByRole("link", { name: "Edit" }).click();
    await page.waitForURL(/\/edit$/);
    await page.waitForLoadState("networkidle");

    // Select the group
    await expandSection(page, "section-organization-trigger");
    const groupSelect = page.getByTestId("check-group-select");
    await expect(groupSelect).toBeVisible({ timeout: 10000 });
    await groupSelect.click();
    const groupOption = page.getByRole("option", { name: groupName });
    await expect(groupOption).toBeVisible({ timeout: 10000 });
    await groupOption.click();

    // Submit
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-[^/]*$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Navigate to checks list and verify check is in the group
    await navigateToChecks(page);

    const groupSection = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(groupName) });
    await expect(groupSection).toBeVisible({ timeout: 10000 });
    await expect(groupSection.getByText(checkName)).toBeVisible({
      timeout: 10000,
    });

    // Clean up
    await deleteGroupViaApi(page, token, group.uid);
  });

  test("should edit a check to remove it from a group", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a group and a check in the group via API
    const token = await getAuthToken(page);
    const ts = Date.now();
    const groupName = `E2E RemoveGroup ${ts}`;
    const group = await createGroupViaApi(page, token, groupName);
    const checkName = `E2E Remove Check ${ts}`;
    const check = await createCheckViaApi(page, token, checkName, group.uid);

    // Navigate to the check edit page
    await page.goto(
      `/dash0/orgs/test/checks/${check.uid}/edit`,
      { waitUntil: "networkidle" }
    );

    // Select "No group" to remove from group
    await expandSection(page, "section-organization-trigger");
    const groupSelect = page.getByTestId("check-group-select");
    await expect(groupSelect).toBeVisible({ timeout: 10000 });
    await groupSelect.click();
    await page.getByRole("option", { name: "No group" }).click();

    // Wait for selection to be reflected
    await expect(groupSelect).toContainText("No group", { timeout: 5000 });

    // Submit
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-[^/]*$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // Navigate to checks list and verify check is NOT in the group
    await navigateToChecks(page);

    // The check should appear in "Ungrouped Checks" now
    await expect(page.getByText(checkName)).toBeVisible({ timeout: 10000 });

    // The group section should not contain this check
    const groupSection = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(groupName) });
    // Group may still exist but should not contain the check
    if (await groupSection.isVisible()) {
      await expect(groupSection.getByText(checkName)).not.toBeVisible();
    }

    // Clean up
    await deleteGroupViaApi(page, token, group.uid);
  });

  test("should show checks in their respective groups", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Create a group, a check in the group, and an ungrouped check via API
    const token = await getAuthToken(page);
    const ts = Date.now();
    const groupName = `E2E Section ${ts}`;
    const group = await createGroupViaApi(page, token, groupName);

    const groupedCheckName = `E2E In Group ${ts}`;
    const ungroupedCheckName = `E2E Ungrouped ${ts}`;
    await createCheckViaApi(page, token, groupedCheckName, group.uid);
    await createCheckViaApi(page, token, ungroupedCheckName);

    await navigateToChecks(page);

    // Verify the group section contains the grouped check
    const groupSection = page
      .getByTestId("group-section")
      .filter({ has: page.getByTestId("group-name").getByText(groupName) });
    await expect(groupSection).toBeVisible({ timeout: 10000 });
    await expect(groupSection.getByText(groupedCheckName)).toBeVisible({
      timeout: 10000,
    });

    // Verify the ungrouped check appears outside the group section (in "Ungrouped Checks")
    await expect(page.getByText(ungroupedCheckName)).toBeVisible({
      timeout: 10000,
    });
    await expect(page.getByText("Ungrouped Checks")).toBeVisible();

    // Take screenshot
    await page.screenshot({
      path: "test-results/screenshots/check-groups-with-checks.png",
      fullPage: true,
    });

    // Clean up
    await deleteGroupViaApi(page, token, group.uid);
  });

  test("should create a check group with an explicit slug", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await navigateToChecks(page);

    await page.getByTestId("new-group-button").click();
    const dialog = page.getByRole("dialog", { name: "New Group" });
    await expect(dialog).toBeVisible();

    const ts = Date.now();
    const groupName = `E2E Explicit Slug ${ts}`;
    const explicitSlug = `e2e-explicit-slug-${ts}`;
    await page.getByTestId("new-group-name-input").fill(groupName);

    // The slug auto-fills from the name; overwrite it with our own value.
    const slugInput = page.getByTestId("new-group-slug-input");
    await expect(slugInput).not.toHaveValue("");
    await slugInput.fill(explicitSlug);

    await page.getByTestId("new-group-submit").click();
    await expect(dialog).not.toBeVisible({ timeout: 10000 });
    await expect(
      page.getByTestId("group-name").getByText(groupName)
    ).toBeVisible({ timeout: 10000 });

    // The group must be reachable by the slug we typed, not an auto-derived one.
    const token = await getAuthToken(page);
    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/check-groups/${explicitSlug}`,
      { headers: { Authorization: `Bearer ${token}` } }
    );
    expect(resp.status()).toBe(200);
    const body = await resp.json();
    expect(body.name).toBe(groupName);

    // Clean up
    await deleteGroupViaApi(page, token, body.uid);
  });

  test("should edit a check group's slug and make it reachable by the new slug", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const token = await getAuthToken(page);
    const ts = Date.now();
    const originalSlug = `e2e-slug-orig-${ts}`;
    const newSlug = `e2e-slug-new-${ts}`;
    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/check-groups`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: { name: `E2E Slug Edit ${ts}`, slug: originalSlug },
      }
    );
    expect(createResp.status()).toBe(201);
    const group = await createResp.json();

    await page.goto(`/dash0/orgs/test/check-groups/${group.uid}/edit`, {
      waitUntil: "networkidle",
    });

    const slugInput = page.getByTestId("group-edit-slug-input");
    await expect(slugInput).toHaveValue(originalSlug, { timeout: 10000 });
    await slugInput.fill(newSlug);
    await page.getByTestId("group-edit-submit").click();

    // Saving navigates back to the checks list.
    await page.waitForURL(/\/checks$/, { timeout: 10000 });

    // The group is now reachable by the new slug...
    const byNewSlug = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/check-groups/${newSlug}`,
      { headers: { Authorization: `Bearer ${token}` } }
    );
    expect(byNewSlug.status()).toBe(200);

    // ...and the old slug stops resolving.
    const byOldSlug = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/check-groups/${originalSlug}`,
      { headers: { Authorization: `Bearer ${token}` } }
    );
    expect(byOldSlug.status()).toBe(404);

    // Clean up
    await deleteGroupViaApi(page, token, group.uid);
  });

  test("should show an inline error for a duplicate slug on the edit page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const token = await getAuthToken(page);
    const ts = Date.now();
    const groupA = await createGroupViaApi(page, token, `E2E DupSlugA ${ts}`);
    const groupB = await createGroupViaApi(page, token, `E2E DupSlugB ${ts}`);

    await page.goto(`/dash0/orgs/test/check-groups/${groupB.uid}/edit`, {
      waitUntil: "networkidle",
    });

    const slugInput = page.getByTestId("group-edit-slug-input");
    await expect(slugInput).toHaveValue(groupB.slug, { timeout: 10000 });
    await slugInput.fill(groupA.slug);
    await page.getByTestId("group-edit-submit").click();

    // The failure must surface inline on the field, not just as a toast, and
    // the page must NOT navigate away (a toast-only failure would still let
    // the "successful" submit redirect back to the list).
    await expect(page.getByTestId("group-edit-slug-error")).toBeVisible({
      timeout: 10000,
    });
    await expect(page).toHaveURL(/\/edit$/);

    // The group must still be reachable by its original slug — the update
    // was rejected, not silently applied.
    const stillOriginal = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/check-groups/${groupB.slug}`,
      { headers: { Authorization: `Bearer ${token}` } }
    );
    expect(stillOriginal.status()).toBe(200);

    // Clean up
    await deleteGroupViaApi(page, token, groupA.uid);
    await deleteGroupViaApi(page, token, groupB.uid);
  });

  test("should leave a check group's slug untouched when only its name is edited", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const token = await getAuthToken(page);
    const ts = Date.now();
    const originalName = `E2E NameOnly ${ts}`;
    const group = await createGroupViaApi(page, token, originalName);

    await page.goto(`/dash0/orgs/test/check-groups/${group.uid}/edit`, {
      waitUntil: "networkidle",
    });

    const nameInput = page.getByTestId("group-edit-name-input");
    await expect(nameInput).toHaveValue(originalName, { timeout: 10000 });
    const newName = `E2E NameOnly Renamed ${ts}`;
    await nameInput.clear();
    await nameInput.fill(newName);

    // Deliberately do not touch the slug field.
    await page.getByTestId("group-edit-submit").click();
    await page.waitForURL(/\/checks$/, { timeout: 10000 });

    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/check-groups/${group.slug}`,
      { headers: { Authorization: `Bearer ${token}` } }
    );
    expect(resp.status()).toBe(200);
    const updated = await resp.json();
    expect(updated.name).toBe(newName);
    expect(updated.slug).toBe(group.slug);

    // Clean up
    await deleteGroupViaApi(page, token, group.uid);
  });
});
