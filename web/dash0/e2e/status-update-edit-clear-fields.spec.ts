import { test, expect, API_BASE } from "./fixtures";
import type { Page } from "@playwright/test";

// Coverage for the "editing a status update silently discards most of the
// changes" bug (specs/todos/2026-08-21-02-status-update-edit-changes-not-saved.md):
//
// 1. Clearing sectionUid/checkUid/linkUrl on the edit form must actually
//    persist as NULL — the assertion proves the negative (the field reads
//    "No section" / "No check" / empty after a reload), not merely that the
//    PATCH request returned 200.
// 2. A second pass that CHANGES values (rather than clearing them), saves,
//    and re-enters the edit page via an IN-APP client-side navigation (never
//    page.goto/reload) guards the stale-cache regression the query-key
//    invalidation fix is for. A real navigation would tear down and recreate
//    the QueryClient (constructed fresh in module scope, main.tsx) and always
//    hit the network with an empty cache — that would never exercise the
//    singular ["statusUpdate", org, uid] cache entry at all, making the
//    assertion pass whether or not the fix exists. Only a client-side
//    transition (clicking the in-app <Link> back into the same edit route)
//    keeps the QueryClient instance alive long enough for the bug to
//    reproduce: without invalidating the singular key alongside the list,
//    react-query would still be within staleTime and would render the
//    pre-edit cached payload.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function api(
  page: Page,
  token: string,
  path: string,
  data: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const resp = await page.request.post(`${API_BASE}${path}`, {
    headers: { Authorization: `Bearer ${token}` },
    data,
  });
  expect(resp.status(), `POST ${path} -> ${await resp.text()}`).toBeLessThan(300);
  return resp.json();
}

test.describe("Status update edit — clearing fields and stale cache", () => {
  test("clearing section/check/link persists as null, and edits survive the singular-cache window", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);

    // --- Seed: a status page with a section and a check-resource ---
    const pageSlug = `e2e-su-edit-${suffix}`.slice(0, 40);
    const statusPage = await api(page, token, "/api/v1/orgs/test/status-pages", {
      name: `E2E SU Edit Page ${suffix}`,
      slug: pageSlug,
      visibility: "public",
    });

    const sectionName = `Core ${suffix}`;
    const section = await api(
      page,
      token,
      `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections`,
      { name: sectionName, slug: `core-${suffix}` },
    );

    const checkName = `e2e-su-edit-check-${suffix}`;
    const check = await api(page, token, "/api/v1/orgs/test/checks", {
      type: "http",
      name: checkName,
      config: { url: `https://httpbin.org/anything/${suffix}` },
      period: "00:05:00",
    });

    await api(
      page,
      token,
      `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections/${section.uid}/resources`,
      { checkUid: check.uid },
    );

    const linkURL = `https://status.example.com/incident/${suffix}`;
    const title = `E2E clear-fields update ${suffix}`;
    const update = await api(page, token, "/api/v1/orgs/test/status-updates", {
      statusPageUid: statusPage.uid,
      sectionUid: section.uid,
      checkUid: check.uid,
      linkUrl: linkURL,
      kind: "investigating",
      title,
      bodyMarkdown: "Original body.",
    });

    // --- Positive control: the edit form loads the non-null baseline ---
    await page.goto(`orgs/test/status-updates/${update.uid}/edit`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("status-update-form-title")).toHaveValue(title);
    await expect(page.getByTestId("status-update-form-section")).toContainText(
      sectionName,
    );
    await expect(page.getByTestId("status-update-form-check")).toContainText(
      checkName,
    );
    await expect(page.getByTestId("status-update-form-link-url")).toHaveValue(
      linkURL,
    );

    // --- Clear section, check and link URL ---
    await page.getByTestId("status-update-form-section").click();
    await page.getByRole("option", { name: "No section", exact: true }).click();

    await page.getByTestId("status-update-form-check").click();
    await page.getByRole("option", { name: "No check", exact: true }).click();

    await page.getByTestId("status-update-form-link-url").fill("");

    await page.getByTestId("status-update-form-submit").click();
    await page.waitForURL(/\/status-updates\/?$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    // --- Negative assertion: reload the edit page and prove the fields
    // really cleared (not just that the request returned 200) ---
    await page.goto(`orgs/test/status-updates/${update.uid}/edit`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("status-update-form-section")).toContainText(
      "No section",
    );
    await expect(page.getByTestId("status-update-form-check")).toContainText(
      "No check",
    );
    await expect(page.getByTestId("status-update-form-link-url")).toHaveValue("");
    // Sibling field untouched by the clears.
    await expect(page.getByTestId("status-update-form-title")).toHaveValue(title);

    // Cross-check directly against the API too (belt and braces on the
    // negative — the UI reading empty must reflect the stored value, not a
    // form default).
    const afterClearResp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/status-updates/${update.uid}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    const afterClear = await afterClearResp.json();
    expect(afterClear.sectionUid).toBeUndefined();
    expect(afterClear.checkUid).toBeUndefined();
    expect(afterClear.linkUrl).toBeUndefined();

    // --- Second pass: change values (not clear them), save, go to the
    // list, then re-enter edit via an in-app click within the 1-minute
    // staleTime window and assert the NEW values render — guards the stale
    // singular-cache regression the query-key invalidation fix is for. ---
    const newTitle = `E2E clear-fields update ${suffix} (changed)`;
    await page.getByTestId("status-update-form-title").fill(newTitle);
    await page.getByTestId("status-update-form-section").click();
    await page.getByRole("option", { name: sectionName, exact: true }).click();
    await page.getByTestId("status-update-form-check").click();
    await page.getByRole("option", { name: checkName, exact: true }).click();

    await page.getByTestId("status-update-form-submit").click();
    await page.waitForURL(/\/status-updates\/?$/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await expect(page.getByText(newTitle)).toBeVisible({ timeout: 10000 });

    // Re-enter the edit page via an IN-APP client-side transition — clicking
    // the row's title <Link> (status-updates.index.tsx), NOT page.goto/reload.
    // The QueryClient instance from the save above must survive this
    // navigation for the assertion below to mean anything: the app-wide
    // singular ["statusUpdate", org, uid] cache entry has to have been
    // invalidated by the save, or react-query would still be within the
    // 1-minute staleTime and would serve the pre-edit cached payload instead
    // of refetching.
    const updatedRow = page
      .getByTestId("status-update-row")
      .filter({ hasText: newTitle });
    await updatedRow.getByTestId("status-update-row-title").click();
    await page.waitForURL(
      new RegExp(`/status-updates/${update.uid}/edit`),
      { timeout: 10000 },
    );

    await expect(page.getByTestId("status-update-form-title")).toHaveValue(
      newTitle,
    );
    await expect(page.getByTestId("status-update-form-section")).toContainText(
      sectionName,
    );
    await expect(page.getByTestId("status-update-form-check")).toContainText(
      checkName,
    );
  });
});
