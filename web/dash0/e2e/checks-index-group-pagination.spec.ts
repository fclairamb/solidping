import { test, expect, API_BASE, type Page } from "./fixtures";

// Regression guard for spec 2026-07-20-05: on the checks index, a group whose
// checks land on a later page must render *skeletons*, never the "No checks in
// this group" empty state, while the page-level infinite query still has more
// to give (hasNextPage true). Before the fix the empty state lied — a populated
// group with a non-zero badge showed "No checks" until the user scrolled to the
// very bottom.
//
// The seed deliberately spans >1 page (limit is capped at 100): three groups
// with explicit sortOrder 0/10/20 hold 60/42/8 label-scoped checks (110 total).
// With sort=group the first page (100) fills group A (60) + 40 of B, so the
// last-rendered group C (badge 8, all on page 2) is empty on first paint — the
// exact false-empty scenario. Deep-linking with the unique label narrows the
// infinite query to just this run's checks so the two-page boundary is
// deterministic regardless of what else lives in the org.

const NO_CHECKS_TEXT = "No checks in this group";

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
  sortOrder: number,
): Promise<{ uid: string; name: string }> {
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/check-groups`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { name, sortOrder },
    },
  );
  expect(resp.status()).toBe(201);
  return resp.json();
}

async function deleteChecksByLabel(
  page: Page,
  token: string,
  label: string,
): Promise<void> {
  const headers = { Authorization: `Bearer ${token}` };
  for (let guard = 0; guard < 30; guard++) {
    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/checks?labels=${label}&limit=100`,
      { headers },
    );
    const body = await resp.json();
    const data: { uid: string }[] = body.data ?? [];
    if (data.length === 0) break;
    for (const c of data) {
      await page.request.delete(
        `${API_BASE}/api/v1/orgs/test/checks/${c.uid}`,
        { headers },
      );
    }
  }
}

test.describe("Checks index group-ordered pagination", () => {
  // Bulk-import 110 checks in one request; give it room.
  test.setTimeout(120_000);

  test("a later-page group shows skeletons, never a false 'No checks'", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    const ts = Date.now();
    const labelKey = "pag";
    const labelValue = `r${ts}`;
    const label = `${labelKey}:${labelValue}`;

    const nameA = `E2E Pag A ${ts}`;
    const nameB = `E2E Pag B ${ts}`;
    const nameC = `E2E Pag C ${ts}`;

    const groups: { uid: string }[] = [];
    try {
      // Groups render top→bottom by sortOrder, so C (20) is the last section.
      const groupA = await createGroup(page, token, nameA, 0);
      const groupB = await createGroup(page, token, nameB, 10);
      const groupC = await createGroup(page, token, nameC, 20);
      groups.push(groupA, groupB, groupC);

      // Build one import document: 60 in A, 42 in B, 8 in C (110 > one page).
      const checks: unknown[] = [];
      const push = (groupName: string, prefix: string, count: number) => {
        for (let i = 0; i < count; i++) {
          checks.push({
            name: `${prefix} ${ts}-${i}`,
            slug: `pag-${ts}-${prefix.toLowerCase()}-${i}`,
            type: "http",
            config: { url: `https://example.com/${prefix}/${i}` },
            enabled: true,
            labels: { [labelKey]: labelValue },
            group: groupName,
          });
        }
      };
      push(nameA, "paga", 60);
      push(nameB, "pagb", 42);
      push(nameC, "pagc", 8);

      const importResp = await page.request.post(
        `${API_BASE}/api/v1/orgs/test/checks/import`,
        {
          headers: { Authorization: `Bearer ${token}` },
          data: {
            version: 1,
            exportedAt: new Date().toISOString(),
            organization: "test",
            checks,
          },
        },
      );
      expect(importResp.status()).toBe(200);
      const importBody = await importResp.json();
      expect(importBody.created).toBe(110);

      // Deep-link with the unique label so the infinite query returns exactly
      // this run's 110 checks (page 1 = 100, page 2 = 10).
      await page.goto(`orgs/test/checks?labels=${label}`);
      await page.waitForLoadState("networkidle");

      // The page rendered: group A's sections are present with rows.
      const sectionByName = (name: string) =>
        page.locator('[data-testid="group-section"]').filter({
          has: page.getByTestId("group-name").filter({ hasText: name }),
        });

      const sectionA = sectionByName(nameA);
      const sectionB = sectionByName(nameB);
      const sectionC = sectionByName(nameC);

      await expect(sectionA).toBeVisible({ timeout: 15_000 });
      await expect(sectionC).toBeVisible();

      // FIRST PAINT (page 1 only, hasNextPage still true): none of the three
      // seeded groups — all with a non-zero badge — may show the false empty
      // state. C is the regression focus: its checks are all on page 2, so it
      // is empty here and must render skeletons, not "No checks".
      for (const section of [sectionA, sectionB, sectionC]) {
        await expect(section.getByText(NO_CHECKS_TEXT)).toHaveCount(0);
      }
      // C is genuinely empty on page 1 → it must be showing skeleton rows.
      await expect(sectionC.locator(".animate-pulse").first()).toBeVisible();
      // Sanity: A (fully on page 1) already shows real rows.
      await expect(page.getByText(`paga ${ts}-0`).first()).toBeVisible();

      // Drain the stream by scrolling to the bottom until C's rows arrive.
      for (let i = 0; i < 15; i++) {
        await page.evaluate(() =>
          window.scrollTo(0, document.body.scrollHeight),
        );
        if (
          await sectionC
            .getByText(`pagc ${ts}-0`)
            .isVisible()
            .catch(() => false)
        ) {
          break;
        }
        await page.waitForTimeout(400);
      }

      // AFTER SCROLL: C's real rows are present and the false empty state never
      // appeared.
      await expect(sectionC.getByText(`pagc ${ts}-0`)).toBeVisible({
        timeout: 15_000,
      });
      await expect(sectionC.getByText(NO_CHECKS_TEXT)).toHaveCount(0);
    } finally {
      await deleteChecksByLabel(page, token, label);
      for (const group of groups) {
        await page.request.delete(
          `${API_BASE}/api/v1/orgs/test/check-groups/${group.uid}`,
          { headers: { Authorization: `Bearer ${token}` } },
        );
      }
    }
  });
});
