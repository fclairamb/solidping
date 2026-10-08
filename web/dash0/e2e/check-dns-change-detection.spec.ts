import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-10-03-04 (DNS change detection): the form turns
// detection on (preselecting "warning" for an A record), the first run of the
// region captures a baseline, a baseline that no longer matches is reported as
// added/removed values on the check page, and "Accept current records" resets
// the baseline, which the next (immediate) run captures again.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

type CheckBody = {
  config: Record<string, unknown>;
  lastResult?: { output?: Record<string, unknown> };
};

test.describe("DNS change detection", () => {
  test("captures a baseline, reports a change and accepts it", async ({ authenticatedPage }) => {
    test.setTimeout(120_000);
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const headers = { Authorization: `Bearer ${token}` };
    const getCheck = async (uid: string): Promise<CheckBody> =>
      (await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${uid}?with=last_result`, { headers })).json();

    await page.goto("orgs/test/checks/new?checkType=dns");
    await page.waitForLoadState("networkidle");
    await page.getByTestId("check-name-input").fill(`E2E dns changes ${Date.now()}`);
    // localhost resolves from the hosts file: no outbound DNS needed in CI.
    await page.getByTestId("check-domain-input").fill("localhost");

    await expect(page.getByTestId("check-dns-on-change-select")).toHaveCount(0);
    await page.getByTestId("check-dns-detect-changes-switch").click();
    // An A record rotates behind load balancers: warning is preselected.
    await expect(page.getByTestId("check-dns-on-change-select")).toContainText("Warn only");
    await expect(page.getByTestId("check-dns-on-change-hint")).toBeVisible();
    await expect(page.getByTestId("check-dns-baseline-empty")).toBeVisible();

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    const created = await getCheck(uid);
    expect(created.config.detect_changes).toBe(true);
    expect(created.config.on_change).toBe("warning");

    // The first run of the region captures its answer as the baseline.
    let region = "";
    await expect
      .poll(
        async () => {
          const baseline = (await getCheck(uid)).config.baseline as
            | Record<string, string[]>
            | undefined;
          region = Object.keys(baseline ?? {})[0] ?? "";
          return baseline?.[region] ?? [];
        },
        { timeout: 60_000, intervals: [1000] },
      )
      .toEqual(["127.0.0.1"]);

    // The form shows the stored baseline per region.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await expect(page.getByTestId("check-dns-baseline")).toContainText(region);
    await expect(page.getByTestId("check-dns-baseline")).toContainText("127.0.0.1");
    await expect(page.getByTestId("check-dns-reset-baseline")).toBeVisible();

    // Pretend the records moved: a baseline that no longer matches, and a
    // short period so the next run comes quickly.
    const patch = await page.request.patch(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers,
      data: {
        period: "00:00:10",
        config: { ...created.config, baseline: { [region]: ["10.9.9.9"] } },
      },
    });
    expect(patch.ok(), await patch.text()).toBeTruthy();

    await expect
      .poll(async () => (await getCheck(uid)).lastResult?.output?.changes ?? null, {
        timeout: 60_000,
        intervals: [1000],
      })
      .toEqual({ added: ["127.0.0.1"], removed: ["10.9.9.9"] });

    await page.goto(`orgs/test/checks/${uid}`);
    const card = page.getByTestId("dns-changes-card");
    await expect(card).toBeVisible();
    await expect(page.getByTestId("dns-changes-added")).toContainText("127.0.0.1");
    await expect(page.getByTestId("dns-changes-removed")).toContainText("10.9.9.9");

    await page.getByTestId("dns-changes-accept").click();

    // The reset baseline is captured again from the current answer.
    await expect
      .poll(
        async () =>
          ((await getCheck(uid)).config.baseline as Record<string, string[]> | undefined)?.[
            region
          ] ?? [],
        { timeout: 60_000, intervals: [1000] },
      )
      .toEqual(["127.0.0.1"]);

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, { headers });
  });
});
