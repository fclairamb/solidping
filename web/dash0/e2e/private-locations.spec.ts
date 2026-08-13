import { test, expect, API_BASE, type Page } from "./fixtures";

// E2E for the Private locations page (spec 2026-07-16-02): create a private
// region, mint an enrollment token (shown once), see it in the check-form
// region picker, and clean up.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

// Remove any leftover region from a previous run so the suite is idempotent.
async function deleteRegionIfExists(page: Page, token: string, slug: string): Promise<void> {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/private-regions/${slug}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

// Regression guard for spec 2026-07-20-02: the enrollment-token dialog's
// long unbreakable content (the hex token, the docker command) used to
// blow out DialogContent's implicit CSS grid track and bleed past the
// dialog's own rounded border. Assert every given testid's bounding box is
// fully contained within the dialog's bounding box (with a 1px slack for
// subpixel rounding).
//
// All rects are read in a single page.evaluate() call, synchronously, in
// one browser-side pass. The dialog animates open (translate + scale over
// ~200ms), so separate awaited boundingBox() calls per element can sample
// different animation frames — the dialog's rect captured a few
// milliseconds before the content's rect — which manifested as a flaky
// few-pixel "overflow" that had nothing to do with layout.
async function assertContainedWithinDialog(page: Page, testIds: string[]): Promise<void> {
  // Let the open animation (duration-200) settle before measuring, so the
  // dialog and its content are read at the same, final, stable geometry.
  await page.waitForTimeout(300);

  const rects = await page.evaluate((ids: string[]) => {
    const rectOf = (el: Element | null) => {
      if (!el) return null;
      const { x, width } = el.getBoundingClientRect();
      return { x, width };
    };
    const dialog = document.querySelector('[role="dialog"]');
    return {
      dialog: rectOf(dialog),
      children: ids.map((id) => rectOf(document.querySelector(`[data-testid="${id}"]`))),
    };
  }, testIds);

  expect(rects.dialog, "dialog should have a bounding box").not.toBeNull();
  if (!rects.dialog) return;
  const dialogBox = rects.dialog;

  testIds.forEach((testId, i) => {
    const box = rects.children[i];
    expect(box, `${testId} should have a bounding box`).not.toBeNull();
    if (!box) return;

    expect(box.x, `${testId} left edge within dialog`).toBeGreaterThanOrEqual(dialogBox.x - 1);
    expect(
      box.x + box.width,
      `${testId} right edge within dialog`,
    ).toBeLessThanOrEqual(dialogBox.x + dialogBox.width + 1);
  });
}

test.describe("Private locations", () => {
  test.beforeEach(async ({ authenticatedPage }) => {
    const token = await getAuthToken(authenticatedPage);
    await deleteRegionIfExists(authenticatedPage, token, "e2e-dc");
  });

  test.afterEach(async ({ authenticatedPage }) => {
    const token = await getAuthToken(authenticatedPage);
    await deleteRegionIfExists(authenticatedPage, token, "e2e-dc");
  });

  test("create region, mint one-shot token, delete region", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/organization/private-locations");
    await page.waitForLoadState("networkidle");

    // Empty state (or at least the create form) renders.
    await expect(page.getByTestId("private-regions-card")).toBeVisible();

    // Create a private region.
    await page.getByTestId("new-region-slug").fill("e2e-dc");
    await page.getByTestId("new-region-name").fill("E2E Datacenter");
    await page.getByTestId("create-region").click();

    const row = page.getByTestId("private-region-e2e-dc");
    await expect(row).toBeVisible();
    // The org-relative region string is shown (the reserved @-namespace, with
    // NO org slug in it — that is what survives an org rename).
    await expect(row).toContainText("@e2e-dc");

    // Mint an enrollment token: the secret is revealed exactly once.
    await page.getByTestId("mint-token-e2e-dc").click();
    const minted = page.getByTestId("minted-token");
    await expect(minted).toBeVisible();
    await expect(minted).toContainText("spe_");

    // Regression guard (spec 2026-07-20-02): the token <code> and the
    // docker <pre> must stay within the dialog's box — no grid blowout.
    await assertContainedWithinDialog(page, ["minted-token", "docker-run-command"]);

    // Close the reveal dialog; the pending-token list shows the token row
    // WITHOUT the secret.
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("pending-tokens-card")).toBeVisible();
    await expect(page.getByTestId("pending-tokens-card")).toContainText("@e2e-dc");
    await expect(page.getByTestId("pending-tokens-card")).not.toContainText("spe_");

    // Cancel the pending token (destructive icon), then delete the region.
    await page
      .getByTestId("pending-tokens-card")
      .locator("[data-testid^='delete-token-']")
      .first()
      .click();

    await page.getByTestId("delete-region-e2e-dc").click();
    await page.getByTestId("confirm-delete-region").click();
    await expect(page.getByTestId("private-region-e2e-dc")).not.toBeVisible();
  });

  test("enrollment token dialog stays within bounds on a narrow viewport", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Create the region via API for speed.
    const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/private-regions`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { slug: "e2e-dc", name: "E2E Datacenter" },
    });
    expect(resp.ok()).toBeTruthy();

    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto("orgs/test/organization/private-locations");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("mint-token-e2e-dc").click();
    const minted = page.getByTestId("minted-token");
    await expect(minted).toBeVisible();
    await expect(minted).toContainText("spe_");

    // Regression guard (spec 2026-07-20-02): same containment check as the
    // desktop test above, but at a mobile viewport width, where the dialog
    // itself is narrower and any grid-blowout bleed would be even more
    // visible relative to the screen.
    await assertContainedWithinDialog(page, ["minted-token", "docker-run-command"]);
  });

  // Regression guard for spec 2026-08-13-07: the "Last seen" agent cell
  // used to render a raw toLocaleString() timestamp; it now shows a
  // live-ticking relative time with the exact timestamp on hover, and
  // still falls back to "never" when lastSeenAt is absent. Stub the agents
  // list (driving a real agent connection over the WS protocol isn't
  // practical from Playwright — same rationale as deported-agent-wizard.spec.ts)
  // and assert the format, not an exact tick value, since it changes every
  // second and would otherwise flake.
  test("agents table shows a live relative last-seen time with an exact-timestamp tooltip", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/private-regions`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { slug: "e2e-dc", name: "E2E Datacenter" },
    });
    expect(resp.ok()).toBeTruthy();

    const lastSeenAt = new Date(Date.now() - 5 * 60_000).toISOString();
    const seenAgent = {
      uid: "e2e-lastseen-agent",
      name: "seen-agent",
      region: "@e2e-dc",
      fingerprint: "fp-seen",
      status: "active",
      enrolledAt: new Date().toISOString(),
      lastSeenAt,
    };
    const neverAgent = {
      uid: "e2e-neverseen-agent",
      name: "never-agent",
      region: "@e2e-dc",
      fingerprint: "fp-never",
      status: "active",
      enrolledAt: new Date().toISOString(),
    };

    await page.route("**/api/v1/orgs/test/agents", async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      await route.fulfill({ json: { data: [seenAgent, neverAgent] } });
    });

    await page.goto("orgs/test/organization/private-locations");
    await page.waitForLoadState("networkidle");

    // Relative time, not the old absolute toLocaleString() format — assert
    // the shape only, never an exact "Ns ago" value (it ticks every second).
    const seenCell = page.getByTestId(`agent-last-seen-${seenAgent.uid}`);
    await expect(seenCell).toBeVisible();
    await expect(seenCell).toHaveText(/ago/i);

    // The exact local timestamp stays reachable via the title tooltip.
    const title = await seenCell.locator("span[title]").getAttribute("title");
    expect(title).toBeTruthy();

    // No lastSeenAt at all still falls back to "never".
    const neverCell = page.getByTestId(`agent-last-seen-${neverAgent.uid}`);
    await expect(neverCell).toHaveText("never");
  });

  test("private region appears in the check-form region picker", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Create the region via API for speed.
    const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/private-regions`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { slug: "e2e-dc", name: "E2E Datacenter" },
    });
    expect(resp.ok()).toBeTruthy();

    await page.goto("orgs/test/checks/new");
    await page.waitForLoadState("networkidle");

    // Pick a check type so the form (and its region picker) renders.
    await page.getByText("HTTP", { exact: false }).first().click();

    // The private region is offered under its org-relative slug with the
    // Private badge.
    const option = page.getByTestId("region-option-@e2e-dc");
    await expect(option).toBeVisible();
    await expect(option).toContainText("E2E Datacenter");
    await expect(option).toContainText("Private");
  });
});
