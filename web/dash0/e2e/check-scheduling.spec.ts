import { test, expect, API_BASE, type Page } from "./fixtures";

/**
 * E2E for the check scheduling page (spec 2026-08-26-04): the surface an org
 * uses to bring its per-minute execution demand back under its cap.
 *
 * What it proves end to end, against a real server:
 *   - the page lists active checks with their per-minute contribution;
 *   - an inline period edit moves the header total BEFORE anything is saved;
 *   - Apply actually PATCHes the check (verified by re-reading the API);
 *   - auto-rebalance renders a proposal and applying it lands under the cap;
 *   - passive checks are excluded from the table and explained in a note.
 */

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function createCheck(
  page: Page,
  token: string,
  data: Record<string, unknown>,
): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data,
  });
  expect(resp.ok(), await resp.text()).toBeTruthy();
  const body = await resp.json();
  return body.uid;
}

async function getCheck(
  page: Page,
  token: string,
  uid: string,
): Promise<Record<string, unknown>> {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  expect(resp.ok()).toBeTruthy();
  return resp.json();
}

async function deleteCheck(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

async function setChecksPerMinute(page: Page, token: string, limit: number) {
  const resp = await page.request.patch(
    `${API_BASE}/api/v1/orgs/test/entitlements`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { limits: { maxChecksPerMinute: limit } },
    },
  );
  expect(resp.ok(), await resp.text()).toBeTruthy();
}

/**
 * Snapshot every check's period, so a rebalance that touches checks this test
 * did not create can be undone.
 *
 * Auto-rebalance is org-wide by design — it stretches whatever is heaviest —
 * and the seeded `test` org is shared with every other suite. Silently
 * rewriting a seeded check's period here would surface as an unrelated
 * failure somewhere else entirely.
 */
async function snapshotPeriods(
  page: Page,
  token: string,
): Promise<Map<string, string>> {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/checks?limit=100`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  expect(resp.ok()).toBeTruthy();
  const body = await resp.json();
  const periods = new Map<string, string>();
  for (const check of body.data ?? []) {
    periods.set(check.uid, check.period);
  }
  return periods;
}

async function restorePeriods(
  page: Page,
  token: string,
  snapshot: Map<string, string>,
  skip: string[],
) {
  const now = await snapshotPeriods(page, token);
  for (const [uid, period] of now) {
    const original = snapshot.get(uid);
    if (skip.includes(uid) || original === undefined || original === period) {
      continue;
    }
    await page.request.patch(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { period: original },
    });
  }
}

// A full-replace PUT is the only reliable way to clear a previously-set cap:
// PATCH ignores nulls, so a stale maxChecksPerMinute would leak into later
// suites and start throttling their checks.
async function resetEntitlements(page: Page, token: string) {
  const resp = await page.request.put(
    `${API_BASE}/api/v1/orgs/test/entitlements`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { limits: {} },
    },
  );
  expect(resp.ok()).toBeTruthy();
}

test.describe("Check scheduling page", () => {
  test("lists active checks, recalculates live, and applies an inline period edit", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();
    const created: string[] = [];

    try {
      // 30s period => 2 executions/min.
      const fastUid = await createCheck(page, token, {
        name: `E2E Sched Fast ${stamp}`,
        slug: `e2e-sched-fast-${stamp}`,
        type: "http",
        period: "00:00:30",
        config: { url: "https://example.com/e2e-sched-fast" },
      });
      created.push(fastUid);

      // A passive check must NOT appear in the table — it costs no budget.
      const heartbeatUid = await createCheck(page, token, {
        name: `E2E Sched Heartbeat ${stamp}`,
        slug: `e2e-sched-hb-${stamp}`,
        type: "heartbeat",
        period: "00:01:00",
        config: {},
      });
      created.push(heartbeatUid);

      // A multi-region check: the regions cell renders only above one region.
      const multiUid = await createCheck(page, token, {
        name: `E2E Sched Multi ${stamp}`,
        slug: `e2e-sched-multi-${stamp}`,
        type: "http",
        period: "00:01:00",
        regions: ["eu", "us"],
        config: { url: "https://example.com/e2e-sched-multi" },
      });
      created.push(multiUid);

      await page.goto("orgs/test/checks/scheduling");
      await page.waitForLoadState("networkidle");

      const row = page.getByTestId(`scheduling-row-${fastUid}`);
      await expect(row).toBeVisible();
      await expect(
        page.getByTestId(`scheduling-contribution-${fastUid}`),
      ).toHaveText("2");

      // The passive check is hidden, and the page says why.
      await expect(
        page.getByTestId(`scheduling-row-${heartbeatUid}`),
      ).toHaveCount(0);
      await expect(page.getByTestId("scheduling-passive-note")).toBeVisible();

      // Regions: shown only when there is more than one. A single-region (or
      // region-less) check must not carry the cell at all — repeating the
      // default on every row would bury the checks that actually multiply the
      // org's demand.
      await expect(
        page.getByTestId(`scheduling-regions-${multiUid}`),
      ).toHaveText("2 regions");
      await expect(
        page.getByTestId(`scheduling-regions-${fastUid}`),
      ).toHaveCount(0);
      // Two regions at a 1-minute period = 2/min, not 1.
      await expect(
        page.getByTestId(`scheduling-contribution-${multiUid}`),
      ).toHaveText("2");

      // The period select must never offer a period the API rejects: http
      // declares no minimum, so it falls back to the global 10s floor.
      await page.getByTestId(`scheduling-period-${fastUid}`).click();
      await expect(
        page.getByRole("option", { name: "5 seconds", exact: true }),
      ).toHaveCount(0);
      await expect(
        page.getByRole("option", { name: "10 seconds", exact: true }),
      ).toBeVisible();
      await page.keyboard.press("Escape");

      const meterTotal = page.getByTestId("check-rate-meter-total");
      const before = Number(await meterTotal.textContent());
      expect(before).toBeGreaterThanOrEqual(2);

      // Inline edit: 30 seconds -> 5 minutes. Nothing is saved yet.
      await page.getByTestId(`scheduling-period-${fastUid}`).click();
      await page.getByRole("option", { name: "5 minutes", exact: true }).click();

      // Live recalculation: the header total drops by 1.8 (2 -> 0.2) with no
      // network write in between.
      await expect(page.getByTestId(`scheduling-diff-${fastUid}`)).toBeVisible();
      await expect(page.getByTestId("scheduling-pending-count")).toBeVisible();
      await expect
        .poll(async () => Number(await meterTotal.textContent()))
        .toBeCloseTo(before - 1.8, 0);

      // Still unsaved server-side.
      const stillFast = await getCheck(page, token, fastUid);
      expect(stillFast.period).toBe("00:00:30");

      // Apply writes it.
      await page.getByTestId("scheduling-apply-button").click();
      await expect(page.getByTestId("scheduling-pending-count")).toHaveCount(0, {
        timeout: 10000,
      });

      await expect
        .poll(
          async () => (await getCheck(page, token, fastUid)).period,
          { timeout: 10000 },
        )
        .toBe("00:05:00");
    } finally {
      for (const uid of created) {
        await deleteCheck(page, token, uid);
      }
      await resetEntitlements(page, token);
    }
  });

  test("auto-rebalance proposes longer periods and applying them lands under the cap", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();
    const created: string[] = [];
    const snapshot = await snapshotPeriods(page, token);

    try {
      // Two 10s checks: 6/min each.
      for (let i = 0; i < 2; i++) {
        created.push(
          await createCheck(page, token, {
            name: `E2E Rebalance ${i} ${stamp}`,
            slug: `e2e-rebalance-${i}-${stamp}`,
            type: "http",
            period: "00:00:10",
            config: { url: `https://example.com/e2e-rebalance-${i}` },
          }),
        );
      }

      // A cap the org is now clearly over.
      await setChecksPerMinute(page, token, 2);

      await page.goto("orgs/test/checks/scheduling");
      await page.waitForLoadState("networkidle");

      // The over-limit banner (spec 03) sends people here, so it renders here
      // too — without its own self-referential link.
      await expect(page.getByTestId("check-rate-limit-banner")).toBeVisible();
      await expect(page.getByTestId("check-rate-meter")).toHaveAttribute(
        "data-over",
        "true",
      );

      await page.getByTestId("scheduling-rebalance-button").click();

      // The proposal renders as a diff before anything is written.
      await expect(
        page.getByTestId("scheduling-proposal-summary"),
      ).toBeVisible();
      for (const uid of created) {
        await expect(page.getByTestId(`scheduling-diff-${uid}`)).toBeVisible();
      }

      // The proposed draft is already under the cap, live.
      await expect(page.getByTestId("check-rate-meter")).toHaveAttribute(
        "data-over",
        "false",
      );

      await page.getByTestId("scheduling-apply-button").click();
      await expect(page.getByTestId("scheduling-pending-count")).toHaveCount(0, {
        timeout: 10000,
      });

      // Every check really moved to a longer period, server-side.
      for (const uid of created) {
        await expect
          .poll(
            async () => (await getCheck(page, token, uid)).period,
            { timeout: 10000 },
          )
          .not.toBe("00:00:10");
      }
    } finally {
      await restorePeriods(page, token, snapshot, created);
      for (const uid of created) {
        await deleteCheck(page, token, uid);
      }
      await resetEntitlements(page, token);
    }
  });

  test("stays usable at a phone width", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });

    await page.goto("orgs/test/checks/scheduling");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("check-rate-meter")).toBeVisible();
    await expect(page.getByTestId("scheduling-rebalance-button")).toBeVisible();
    await expect(page.getByTestId("scheduling-apply-button")).toBeVisible();

    // The wide table scrolls inside its own container; the PAGE must not.
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });

  test("is reachable from the checks list", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("scheduling-link").click();
    await page.waitForURL(/\/checks\/scheduling/, { timeout: 10000 });
    await expect(page.getByTestId("check-rate-meter")).toBeVisible();
  });

  test("breadcrumb shows a Scheduling leaf and the Checks crumb links back", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks/scheduling");
    await page.waitForLoadState("networkidle");

    const header = page.locator("header");

    // The leaf crumb names the page — the regression was a bare, non-clickable
    // "Checks" crumb, identical to what the list page renders.
    await expect(header.getByText(/^scheduling$/i)).toBeVisible();

    // And the section crumb becomes a link back to the list.
    const checksCrumb = header.getByRole("link", { name: /^checks$/i });
    await expect(checksCrumb).toBeVisible();
    await checksCrumb.click();
    await page.waitForURL(/\/checks\/?(\?|$)/, { timeout: 10000 });
  });
});
