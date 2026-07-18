import { test, expect, API_BASE, type Page } from "./fixtures";

// Regression guard for spec 2026-07-17-02: the checks list must issue ONE
// batched request for the whole page, not one per group section.
//
// Before the fix, each CheckGroupSection (and the ungrouped section) owned its
// own useInfiniteChecks query, so opening the page fired (groups + 1) requests
// and every live-hint invalidation re-fired all of them — ~(N+1)×20 req/min per
// tab. This test proves the *negative* (the fan-out is gone): it seeds N group
// sections and asserts the checks-list request count does NOT scale with N.
//
// It is a true negative test — against the pre-change per-group code it goes
// red: that code renders one section per org group and fires one labelled query
// per section (≥ N requests), whereas the batched page fires a single request
// regardless of group count. The unique label narrows the batched query to just
// this run's checks (one page, so no pagination noise) while leaving the number
// of rendered group sections — and thus the pre-change fan-out — untouched.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function createGroup(
  page: Page,
  token: string,
  name: string,
): Promise<{ uid: string }> {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/check-groups`,
    { headers: { Authorization: `Bearer ${token}` }, data: { name } },
  );
  expect(resp.status()).toBe(201);
  return resp.json();
}

async function createCheck(
  page: Page,
  token: string,
  name: string,
  checkGroupUid: string,
  labels: Record<string, string>,
): Promise<{ uid: string }> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      type: "http",
      name,
      config: { url: `https://example.com/${Date.now()}-${Math.random()}` },
      period: "00:05:00",
      checkGroupUid,
      labels,
    },
  });
  expect(resp.status()).toBe(201);
  return resp.json();
}

async function deleteGroup(page: Page, token: string, uid: string): Promise<void> {
  await page.request.delete(
    `${API_BASE}/api/v1/orgs/test/check-groups/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
}

test.describe("Checks page request fan-out", () => {
  test("the checks list issues one batched request, not one per group", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const GROUP_COUNT = 8;
    const ts = Date.now();
    const labelKey = "fanout";
    const labelValue = `run-${ts}`;
    const groups: { uid: string }[] = [];

    try {
      for (let i = 0; i < GROUP_COUNT; i++) {
        const group = await createGroup(page, token, `E2E Fanout ${ts}-${i}`);
        groups.push(group);
        await createCheck(
          page,
          token,
          `E2E Fanout Check ${ts}-${i}`,
          group.uid,
          { [labelKey]: labelValue },
        );
      }

      // Count GETs to the checks *list* endpoint only — path ends in `/checks`,
      // which excludes per-check detail (`/checks/{uid}`), `/checks/export`, and
      // `/check-groups`.
      let checksListRequests = 0;
      const onRequest = (req: { url: () => string; method: () => string }) => {
        if (req.method() !== "GET") return;
        let parsed: URL;
        try {
          parsed = new URL(req.url());
        } catch {
          return;
        }
        if (parsed.pathname.endsWith("/checks")) checksListRequests += 1;
      };
      page.on("request", onRequest);

      // Deep-link with the unique label so the batched query returns exactly
      // this run's checks (GROUP_COUNT < limit 100 → a single page, no
      // pagination). The label filter does NOT reduce how many group *sections*
      // render, so the pre-change per-group code still fanned out one request
      // per section here.
      await page.goto(`orgs/test/checks?labels=${labelKey}:${labelValue}`);
      await page.waitForLoadState("networkidle");

      // The page fully rendered its sections (a seeded check is visible).
      await expect(
        page.getByText(`E2E Fanout Check ${ts}-0`),
      ).toBeVisible({ timeout: 10000 });
      // Small settle window to capture any synchronous per-section fan-out.
      await page.waitForTimeout(1000);

      page.off("request", onRequest);

      // Batched model: one list request (allow a little slack for an incidental
      // live-ack refetch). The pre-change model fired at least GROUP_COUNT
      // requests — one per group section — so this assertion is red against it.
      expect(
        checksListRequests,
        `checks-list request count (${checksListRequests}) must not scale with the ${GROUP_COUNT} group sections`,
      ).toBeLessThan(GROUP_COUNT);
    } finally {
      for (const group of groups) await deleteGroup(page, token, group.uid);
    }
  });
});
