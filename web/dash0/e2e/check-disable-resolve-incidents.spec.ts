import {
  test,
  expect,
  API_BASE,
  createHeartbeatCheck,
  getAuthToken,
  uniqueStamp,
} from "./fixtures";

// Spec 2026-10-23-01: disabling a check with an open incident offers to resolve
// it in the same action; the incident then ends as "check_disabled".

test.describe("Disable a check with open incidents", () => {
  test("keeping the checkbox resolves the incident with check_disabled", async ({
    authenticatedPage: page,
  }) => {
    const token = await getAuthToken(page);
    const auth = { Authorization: `Bearer ${token}` };
    const check = await createHeartbeatCheck(page, token, `E2E Disable ${uniqueStamp()}`);

    try {
      const beat = await page.request.get(
        `${API_BASE}/api/v1/heartbeat/test/${check.uid}?token=${check.hbToken}&status=down`,
      );
      expect(beat.status()).toBe(200);

      const listActive = async () => {
        const resp = await page.request.get(
          `${API_BASE}/api/v1/orgs/test/incidents?checkUid=${check.uid}&state=active`,
          { headers: auth },
        );
        return (await resp.json()).data ?? [];
      };
      await expect.poll(async () => (await listActive()).length, { timeout: 15_000 }).toBe(1);

      await page.goto(`/d/orgs/test/checks/${check.uid}`);
      await page.getByTestId("check-toggle-enabled").click();

      await expect(page.getByTestId("disable-check-dialog")).toBeVisible();
      await expect(page.getByTestId("disable-check-resolve-checkbox")).toBeChecked();
      await page.getByTestId("disable-check-confirm").click();

      await expect.poll(async () => (await listActive()).length, { timeout: 10_000 }).toBe(0);

      const resolved = await page.request.get(
        `${API_BASE}/api/v1/orgs/test/incidents?checkUid=${check.uid}&state=resolved`,
        { headers: auth },
      );
      const rows = (await resolved.json()).data;
      expect(rows).toHaveLength(1);
      expect(rows[0].resolutionType).toBe("check_disabled");

      await page.goto(`/d/orgs/test/incidents/${rows[0].uid}`);
      await expect(page.getByTestId("incident-timeline-resolved-reason")).toContainText(
        /disabled/i,
      );
    } finally {
      await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${check.uid}`, {
        headers: auth,
      });
    }
  });

  test("unchecking the box disables the check and leaves the incident open", async ({
    authenticatedPage: page,
  }) => {
    const token = await getAuthToken(page);
    const auth = { Authorization: `Bearer ${token}` };
    const check = await createHeartbeatCheck(page, token, `E2E Disable Keep ${uniqueStamp()}`);

    try {
      await page.request.get(
        `${API_BASE}/api/v1/heartbeat/test/${check.uid}?token=${check.hbToken}&status=down`,
      );
      const listActive = async () => {
        const resp = await page.request.get(
          `${API_BASE}/api/v1/orgs/test/incidents?checkUid=${check.uid}&state=active`,
          { headers: auth },
        );
        return (await resp.json()).data ?? [];
      };
      await expect.poll(async () => (await listActive()).length, { timeout: 15_000 }).toBe(1);

      await page.goto(`/d/orgs/test/checks/${check.uid}`);
      await page.getByTestId("check-toggle-enabled").click();
      await page.getByTestId("disable-check-resolve-checkbox").click();
      await page.getByTestId("disable-check-confirm").click();

      await expect
        .poll(async () => {
          const resp = await page.request.get(
            `${API_BASE}/api/v1/orgs/test/checks/${check.uid}`,
            { headers: auth },
          );
          return (await resp.json()).enabled;
        })
        .toBe(false);
      expect(await listActive()).toHaveLength(1);
    } finally {
      await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${check.uid}`, {
        headers: auth,
      });
    }
  });
});
