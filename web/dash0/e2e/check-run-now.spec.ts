import { API_BASE, DASH_BASE, expect, getAuthToken, test, uniqueStamp } from "./fixtures";

// Spec 2026-10-04-01: "Run now" on the check page header. The button runs the
// check once in every region, spins until a fresh result has landed, then says
// so. The Go route tests pin the real 403 for a viewer; here a 403 answer is
// forced on the wire to pin what the dashboard shows for it.
test.describe("Run now", () => {
  test("runs an http check and shows the new result", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const created = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: `E2E run now ${uniqueStamp()}`,
        type: "http",
        placement: "pinned",
        regions: ["default"],
        // An hour: only the on-demand run can produce a result in this test.
        period: "01:00:00",
        config: { url: `${API_BASE}/api/mgmt/health` },
      },
    });
    expect(created.status(), await created.text()).toBe(201);
    const { uid } = (await created.json()) as { uid: string };

    try {
      await page.goto(`${DASH_BASE}/orgs/test/checks/${uid}`);

      const button = page.getByTestId("check-run-now");
      await expect(button).toBeEnabled();

      // A health check answers in milliseconds, so the run would settle before
      // the pending state can be observed. Hold the watcher's polls
      // (the only results requests that carry periodStartAfter) until released.
      let release: () => void = () => {};
      const gate = new Promise<void>((resolve) => {
        release = resolve;
      });
      await page.route(/\/results\?.*periodStartAfter/, async (route) => {
        await gate;
        await route.continue().catch(() => {});
      });

      const accepted = page.waitForResponse(
        (resp) =>
          resp.url().endsWith(`/checks/${uid}/run-now`) && resp.request().method() === "POST",
      );
      await button.click();
      expect((await accepted).status()).toBe(200);

      // Pending state, then the outcome.
      await expect(page.getByText("Run requested.")).toBeVisible();
      await expect(button).toBeDisabled();
      release();
      await expect(page.getByText(/Run finished: (up|down)\./)).toBeVisible({ timeout: 60_000 });
      await expect(button).toBeEnabled();
    } finally {
      await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
    }
  });

  test("a refused request says Permission Denied", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.route("**/run-now", (route) =>
      route.fulfill({
        status: 403,
        contentType: "application/json",
        body: JSON.stringify({ title: "Forbidden", code: "FORBIDDEN" }),
      }),
    );

    const token = await getAuthToken(page);
    const created = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: `E2E run now denied ${uniqueStamp()}`,
        type: "http",
        placement: "pinned",
        regions: ["default"],
        period: "01:00:00",
        config: { url: "https://acme.com/health" },
      },
    });
    expect(created.status(), await created.text()).toBe(201);
    const { uid } = (await created.json()) as { uid: string };

    try {
      await page.goto(`${DASH_BASE}/orgs/test/checks/${uid}`);
      await page.getByTestId("check-run-now").click();
      await expect(page.getByText(/Permission Denied/)).toBeVisible();
    } finally {
      await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
    }
  });
});
