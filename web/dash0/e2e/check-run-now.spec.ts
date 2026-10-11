import { API_BASE, DASH_BASE, expect, getAuthToken, test, uniqueStamp } from "./fixtures";

// Spec 2026-10-04-01: "Run now" on the check page header. The button runs the
// check once in every region, spins until a fresh result has landed, then says
// so. The Go route tests pin the real 403 for a viewer; here a 403 answer is
// forced on the wire to pin what the dashboard shows for it.
test.describe("Run now", () => {
  test("runs an http check and shows the new result", async ({ authenticatedPage }) => {
    // The outcome assertion below allows 60 s; the default 30 s test timeout
    // used to cut it short with no hint of what was being waited for.
    test.setTimeout(90_000);
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
      // Creating the check fires its first run (the express path). If Run now
      // lands while that run still holds its lease, checkrunnow.RunNow reports
      // it running and queues nothing (the allRunning branch), and that run's
      // result predates the request, so the button can wait for a run that
      // never comes: the likely cause of a 30 s hang in CI run 38110858942,
      // clicked 0.8 s after the create. This test is about an idle check, so
      // wait for that first result before pressing.
      await expect
        .poll(
          async () => {
            const resp = await page.request.get(
              `${API_BASE}/api/v1/orgs/test/checks/${uid}?with=last_result`,
              { headers: { Authorization: `Bearer ${token}` } },
            );
            return ((await resp.json()) as { lastResult?: unknown }).lastResult != null;
          },
          { timeout: 20_000 },
        )
        .toBe(true);

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
      // Creating the check fires its first run (the express path). If Run now
      // lands while that run still holds its lease, checkrunnow.RunNow reports
      // it running and queues nothing (the allRunning branch), and that run's
      // result predates the request, so the button can wait for a run that
      // never comes: the likely cause of a 30 s hang in CI run 38110858942,
      // clicked 0.8 s after the create. This test is about an idle check, so
      // wait for that first result before pressing.
      await expect
        .poll(
          async () => {
            const resp = await page.request.get(
              `${API_BASE}/api/v1/orgs/test/checks/${uid}?with=last_result`,
              { headers: { Authorization: `Bearer ${token}` } },
            );
            return ((await resp.json()) as { lastResult?: unknown }).lastResult != null;
          },
          { timeout: 20_000 },
        )
        .toBe(true);

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
