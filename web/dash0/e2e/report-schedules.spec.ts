import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-08-20-01: the scheduled uptime-report CRUD surface
// under Organization -> Uptime reports.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

test.describe("Uptime report schedules", () => {
  test("creates a schedule and lists it without exposing the addresses", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const name = `E2E Report ${Date.now()}`;
    const recipient = `e2e-${Date.now()}@acme.com`;

    await page.goto("orgs/test/organization/report-schedules");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("report-new").click();
    await page.waitForURL(/\/report-schedules\/new/, { timeout: 10000 });

    await page.getByTestId("report-name").fill(name);

    // Recipients are required — a digest with nobody to send to is not a digest.
    await expect(page.getByTestId("report-submit")).toBeDisabled();

    const recipientsInput = page
      .getByTestId("report-recipients")
      .locator("input")
      .first();
    await recipientsInput.fill(recipient);
    await recipientsInput.press("Enter");

    await expect(page.getByTestId("report-submit")).toBeEnabled();
    await page.getByTestId("report-submit").click();

    await page.waitForURL(/\/report-schedules\/[0-9a-f-]{36}/, {
      timeout: 10000,
    });
    const uid = page.url().match(/\/report-schedules\/([0-9a-f-]{36})/)![1];

    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/report-schedules/${uid}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    expect(resp.status()).toBe(200);
    const created = await resp.json();
    expect(created.name).toBe(name);
    expect(created.recipients).toEqual([recipient]);
    expect(created.frequency).toBe("monthly");

    // The list page shows a COUNT, never the addresses: recipients are PII and
    // a list view is the easiest place for them to be shoulder-surfed.
    await page.goto("orgs/test/organization/report-schedules");
    await page.waitForLoadState("networkidle");

    const row = page.getByTestId("report-row").filter({ hasText: name }).first();
    await expect(row).toBeVisible();
    await expect(row).toContainText("1 recipient");
    await expect(row).not.toContainText(recipient);

    await page.request.delete(
      `${API_BASE}/api/v1/orgs/test/report-schedules/${uid}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
  });

  // Regression coverage for spec 2026-08-21-04: TestSend queues correctly and
  // returns 202 with an empty body, but apiFetch's handleResponse only ever
  // special-cased 204 — so response.json() threw on the empty stream and the
  // UI reported a manufactured failure for a request that had actually
  // succeeded. Asserting the failure toast is ABSENT (not just that the
  // success toast is present) is what would have caught that: both toasts
  // firing together would still pass an assertion that only checked success.
  test("sends a test report and shows success, not a manufactured failure", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const name = `E2E Test Send ${Date.now()}`;
    const recipient = `e2e-test-send-${Date.now()}@acme.com`;

    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/report-schedules`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: { name, frequency: "monthly", recipients: [recipient] },
      },
    );
    expect(createResp.status()).toBe(201);
    const { uid } = await createResp.json();

    try {
      // "Send me a test" lives on the edit page — per the design reference,
      // list rows carry only the Pencil/Trash2 icon pair.
      await page.goto(`orgs/test/organization/report-schedules/${uid}`);
      await page.waitForLoadState("networkidle");

      await page.getByTestId("report-test").click();

      await expect(
        page.getByText("Test report queued to your address"),
      ).toBeVisible();
      await expect(
        page.getByText("Failed to send the test report"),
      ).toHaveCount(0);
    } finally {
      await page.request.delete(
        `${API_BASE}/api/v1/orgs/test/report-schedules/${uid}`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
    }
  });
});

test.describe("Organization breadcrumb", () => {
  // Coverage for spec 2026-08-21-03: every tab under Organization must
  // resolve a breadcrumb leaf, not just the original four (members,
  // invitations, usage, settings).
  test("resolves a leaf on every Organization tab", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const header = () => page.locator("header").first();

    for (const [path, label] of [
      ["requests", "Requests"],
      ["private-locations", "Private locations"],
      ["report-schedules", "Uptime reports"],
    ] as const) {
      await page.goto(`orgs/test/organization/${path}`);
      await page.waitForLoadState("networkidle");
      // Section root on its own index page renders as plain (non-link) text
      // — the negative control that keeps "the leaf is visible" from passing
      // vacuously on a blank page.
      await expect(header().getByText(label, { exact: true })).toBeVisible();
      await expect(header().getByRole("link", { name: label })).toHaveCount(0);
    }
  });

  test("renders the full trail on deep Organization routes", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const header = () => page.locator("header").first();

    // report-schedules/new: section crumb becomes a link, leaf is "New".
    await page.goto("orgs/test/organization/report-schedules/new");
    await page.waitForLoadState("networkidle");
    await expect(
      header().getByRole("link", { name: "Uptime reports" }),
    ).toBeVisible();
    await expect(header().getByText("New", { exact: true })).toBeVisible();

    // report-schedules/$uid: leaf names the record.
    const name = `E2E Breadcrumb Report ${Date.now()}`;
    const createResp = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/report-schedules`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          name,
          frequency: "monthly",
          recipients: [`e2e-breadcrumb-${Date.now()}@acme.com`],
        },
      },
    );
    expect(createResp.status()).toBe(201);
    const { uid } = await createResp.json();

    try {
      await page.goto(`orgs/test/organization/report-schedules/${uid}`);
      await page.waitForLoadState("networkidle");
      await expect(
        header().getByRole("link", { name: "Uptime reports" }),
      ).toBeVisible();
      await expect(header().getByText(name, { exact: true })).toBeVisible();
    } finally {
      await page.request.delete(
        `${API_BASE}/api/v1/orgs/test/report-schedules/${uid}`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
    }

    // private-locations/register: section crumb becomes a link, leaf is
    // "Register".
    await page.goto("orgs/test/organization/private-locations/register");
    await page.waitForLoadState("networkidle");
    await expect(
      header().getByRole("link", { name: "Private locations" }),
    ).toBeVisible();
    await expect(header().getByText("Register", { exact: true })).toBeVisible();
  });
});
