import { test, expect, API_BASE, type Page } from "./fixtures";

// Covers spec 2026-08-01-06: the checks-list Import flow now offers a source
// picker (native SolidPing export + Gatus / Better Stack / Uptime Kuma) and the
// existing dry-run preview dialog additionally lists conversion warnings.
//
// The Gatus path is the E2E happy path: pick the source, paste a config, see
// the preview (counts + warnings), confirm, and find the check in the list.
// Per-source mapping fidelity is covered by the Go golden-file and endpoint
// integration tests — this only proves the UI wiring.

const GATUS_CONFIG = `endpoints:
  - name: e2e gatus api
    group: E2E Gatus
    url: "https://e2e-gatus.example.com/health"
    interval: 5m
    conditions:
      - "[STATUS] == 200"
      - "[RESPONSE_TIME] < 300"
`;

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function deleteCheckBySlug(page: Page, token: string, slug: string): Promise<void> {
  const headers = { Authorization: `Bearer ${token}` };
  const resp = await page.request.get(`${API_BASE}/api/v1/orgs/test/checks/${slug}`, {
    headers,
  });
  if (!resp.ok()) return;
  const check = await resp.json();
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${check.uid}`, { headers });
}

test.describe("Checks import sources", () => {
  test("imports a Gatus config through the source picker and preview dialog", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // Start clean so the preview reports a create, not an update.
    await deleteCheckBySlug(page, token, "e2e-gatus-api");

    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("import-button").click();

    // The source picker offers the native export plus the three converters.
    const sourcePicker = page.getByTestId("import-source");
    await expect(sourcePicker).toBeVisible();
    await sourcePicker.click();
    await expect(page.getByTestId("import-source-gatus")).toBeVisible();
    await expect(page.getByTestId("import-source-betterstack")).toBeVisible();
    await expect(page.getByTestId("import-source-uptime-kuma")).toBeVisible();
    await page.getByTestId("import-source-gatus").click();

    await page.getByTestId("import-payload").fill(GATUS_CONFIG);
    await page.getByTestId("import-preview-button").click();

    // The dry-run preview: one check to create, and the unmappable
    // [RESPONSE_TIME] condition surfaced as a conversion warning.
    const preview = page.getByTestId("import-preview");
    await expect(preview).toBeVisible();
    await expect(page.getByTestId("import-preview-created")).toHaveText("1");
    await expect(page.getByTestId("import-preview-warnings")).toContainText("[RESPONSE_TIME]");

    await page.getByTestId("import-confirm-button").click();

    // The imported check lands in the list.
    await expect(page.getByText("e2e gatus api").first()).toBeVisible({ timeout: 10000 });

    await deleteCheckBySlug(page, token, "e2e-gatus-api");
  });

  test("shows the not-stored notice for the Better Stack token field", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("import-button").click();
    await page.getByTestId("import-source").click();
    await page.getByTestId("import-source-betterstack").click();

    // Better Stack takes a token instead of a file, and says so.
    await expect(page.getByTestId("import-token")).toBeVisible();
    await expect(page.getByTestId("import-payload")).toHaveCount(0);
    await expect(page.getByText("The token is not stored")).toBeVisible();

    // Preview stays disabled until a token is entered.
    await expect(page.getByTestId("import-preview-button")).toBeDisabled();
    await page.getByTestId("import-token").fill("bstk_not_a_real_token");
    await expect(page.getByTestId("import-preview-button")).toBeEnabled();
  });
});
