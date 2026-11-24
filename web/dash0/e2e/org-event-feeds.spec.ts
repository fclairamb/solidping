/**
 * Private org event feeds (spec 2026-11-24-12, #1014): an admin creates a
 * feed from Organization → Settings, copies its URL (shown once), the URL
 * serves Atom without a session, and revoking it makes the URL answer 410.
 */
import { test, expect, API_BASE, getAuthToken, uniqueStamp, type Page } from "./fixtures";

/** Revokes every feed of the shared `test` org, so the 10-feed cap never
 *  leaks from one run into the next. */
async function revokeAllFeeds(page: Page): Promise<void> {
  const token = await getAuthToken(page);
  const headers = { Authorization: `Bearer ${token}` };
  const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/event-feeds`, { headers });
  if (!resp.ok()) return;
  const body = (await resp.json()) as { data: { uid: string }[] };
  for (const feed of body.data) {
    await page.request.delete(`${API_BASE}/api/v1/orgs/test/event-feeds/${feed.uid}`, { headers });
  }
}

test.describe("Organization event feeds", () => {
  test.afterEach(async ({ authenticatedPage }) => {
    await revokeAllFeeds(authenticatedPage);
  });

  test("create, copy and revoke a private feed", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    await revokeAllFeeds(page);

    await page.goto("orgs/test/organization/settings");
    const card = page.getByTestId("event-feeds-card");
    await expect(card).toBeVisible();
    await expect(card.getByTestId("event-feeds-empty")).toBeVisible();

    // The default scope is preselected: incidents and check lifecycle.
    await expect(card.getByTestId("event-feed-scope-checkbox-incident_all")).toBeChecked();
    await expect(card.getByTestId("event-feed-scope-checkbox-status_update_all")).not.toBeChecked();

    const name = `NOC wall ${uniqueStamp()}`;
    await card.getByTestId("event-feed-name-input").fill(name);
    const [createResp] = await Promise.all([
      page.waitForResponse(
        (resp) => resp.url().endsWith("/api/v1/orgs/test/event-feeds") && resp.request().method() === "POST",
      ),
      card.getByTestId("event-feed-create").click(),
    ]);
    expect(createResp.status()).toBe(201);
    const created = (await createResp.json()) as { uid: string };

    // The URL is shown once; copy it.
    const secretBlock = card.getByTestId("event-feed-secret");
    await expect(secretBlock).toBeVisible();
    const urlBlock = secretBlock.getByTestId("event-feed-url");
    await urlBlock.getByRole("button").click();
    const copied = await page.evaluate(() => navigator.clipboard.readText());
    expect(copied).toMatch(/\/api\/v1\/org-feeds\/spf_[A-Za-z0-9_-]{43}\/feed\.xml$/);
    await expect(urlBlock).toContainText(copied);

    // The URL serves Atom without any session.
    const feedPath = new URL(copied).pathname;
    const feedResp = await page.request.fetch(`${API_BASE}${feedPath}`, {
      headers: { Authorization: "" },
    });
    expect(feedResp.status()).toBe(200);
    expect(feedResp.headers()["content-type"]).toContain("application/atom+xml");
    expect(await feedResp.text()).toContain("<feed");

    // Once dismissed, the URL is gone from the page; the feed stays listed.
    await secretBlock.getByTestId("event-feed-secret-dismiss").click();
    await expect(card.getByTestId("event-feed-secret")).toHaveCount(0);
    const item = card.getByTestId(`event-feed-item-${created.uid}`);
    await expect(item.getByTestId("event-feed-name")).toHaveText(name);
    await expect(card).not.toContainText(copied);

    // Revoke: the URL answers 410 Gone.
    page.once("dialog", (dialog) => void dialog.accept());
    await item.getByTestId("event-feed-revoke").click();
    await expect(item).toHaveCount(0);
    await expect(card.getByTestId("event-feeds-empty")).toBeVisible();

    const goneResp = await page.request.fetch(`${API_BASE}${feedPath}`);
    expect(goneResp.status()).toBe(410);
  });
});
