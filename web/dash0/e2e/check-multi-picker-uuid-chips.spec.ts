import { test, expect, API_BASE, type Page } from "./fixtures";

// Regression coverage for spec 2026-08-28-05: CheckMultiPicker chips must
// show a check's NAME even when the check falls outside the current search
// page (CheckMultiPicker's default fetch is capped at 25 results). Before
// the fix, a chip for a selected check outside that page rendered the raw
// UUID instead — reproducible only once an org has more than ~25 checks,
// which is exactly what this test seeds.
//
// Runs against the test org (test@test.com / test). Requires the server in
// SP_RUNMODE=test; the :4000 devloop is normal mode, so this is authored to
// run under `make test-dash` (which boots a test-mode server).

const UUID_PATTERN = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;

// The checks list defaults to `created_at DESC` (newest first, see
// applyChecksOrdering in server/internal/db/{postgres,sqlite}). Creating a
// target check and then this many MORE checks after it guarantees the target
// sits beyond CheckMultiPicker's SEARCH_LIMIT=25 default page, regardless of
// how many other checks the org already had.
const PUSH_DOWN_COUNT = 30;

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function createCheck(
  page: Page,
  token: string,
  name: string,
): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      type: "http",
      name,
      config: { url: `https://acme.com/${name}` },
      period: "00:05:00",
    },
  });
  expect(resp.status()).toBe(201);
  const body = await resp.json();
  return body.uid;
}

async function deleteCheck(page: Page, token: string, uid: string): Promise<void> {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

// Seeds a target check plus PUSH_DOWN_COUNT newer checks after it, so the
// target is guaranteed to fall outside the default (no-search-query) first
// page of results. Returns the target's uid/name and a cleanup function.
async function seedCheckBeyondFirstPage(
  page: Page,
  token: string,
  label: string,
): Promise<{ uid: string; name: string; cleanup: () => Promise<void> }> {
  const stamp = `${label}-${Date.now()}`;
  const name = `acme-target-${stamp}`;
  const uid = await createCheck(page, token, name);

  const pushDownUids = await Promise.all(
    Array.from({ length: PUSH_DOWN_COUNT }, (_, i) =>
      createCheck(page, token, `acme-newer-${stamp}-${i}`),
    ),
  );

  return {
    uid,
    name,
    cleanup: async () => {
      await Promise.all(
        [uid, ...pushDownUids].map((u) => deleteCheck(page, token, u)),
      );
    },
  };
}

test.describe("CheckMultiPicker chips for checks outside the search page", () => {
  test("report-schedule edit page shows the check's name, not its uuid", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const { uid, name, cleanup } = await seedCheckBeyondFirstPage(
      page,
      token,
      "report",
    );

    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/report-schedules`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          name: `E2E chip resolution ${Date.now()}`,
          frequency: "monthly",
          recipients: [`e2e-chip-${Date.now()}@acme.com`],
          checkUids: [uid],
        },
      },
    );
    expect(createResp.status()).toBe(201);
    const { uid: scheduleUid } = await createResp.json();

    try {
      await page.goto(`orgs/test/organization/report-schedules/${scheduleUid}`);
      await page.waitForLoadState("networkidle");

      const picker = page.getByTestId("report-checks");
      await expect(picker.getByTestId(`report-checks-chip-remove-${uid}`)).toBeVisible();

      // Positive: the chip shows the check's name.
      await expect(picker).toContainText(name);

      // Negative control: nothing in the picker (chip text) reads as a raw
      // UUID — the exact regression this spec fixes.
      await expect(picker).not.toHaveText(UUID_PATTERN);
      const pickerText = (await picker.textContent()) ?? "";
      expect(pickerText).not.toMatch(UUID_PATTERN);
    } finally {
      await page.request.delete(
        `${API_BASE}/api/v1/orgs/test/report-schedules/${scheduleUid}`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      await cleanup();
    }
  });

  // The maintenance-window form uses the same CheckMultiPicker component;
  // a lighter pass confirming it benefits from the same fix.
  test("maintenance-window edit page shows the check's name, not its uuid", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const { uid, name, cleanup } = await seedCheckBeyondFirstPage(
      page,
      token,
      "mw",
    );

    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/maintenance-windows`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          title: `E2E chip resolution mw ${Date.now()}`,
          startAt: "2030-01-01T02:00:00Z",
          endAt: "2030-01-01T04:00:00Z",
          recurrence: "none",
        },
      },
    );
    expect(createResp.status()).toBe(201);
    const { uid: windowUid } = await createResp.json();

    const setChecksResp = await page.request.put(
      `${API_BASE}/api/v1/orgs/test/maintenance-windows/${windowUid}/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: { checkUids: [uid], checkGroupUids: [] },
      },
    );
    expect(setChecksResp.status()).toBeLessThan(300);

    try {
      await page.goto(`orgs/test/maintenance-windows/${windowUid}/edit`);
      await page.waitForLoadState("networkidle");

      const picker = page.getByTestId("mw-checks-select");
      await expect(
        picker.getByTestId(`mw-checks-select-chip-remove-${uid}`),
      ).toBeVisible();

      await expect(picker).toContainText(name);

      const pickerText = (await picker.textContent()) ?? "";
      expect(pickerText).not.toMatch(UUID_PATTERN);
    } finally {
      await page.request.delete(
        `${API_BASE}/api/v1/orgs/test/maintenance-windows/${windowUid}`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      await cleanup();
    }
  });
});
