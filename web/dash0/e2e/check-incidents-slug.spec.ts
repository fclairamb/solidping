import { test, expect, type Page } from "./fixtures";

// Regression coverage for issue #127 / spec 2026-07-14-03: the check detail
// page fires `GET /incidents?checkUid=$checkUid` using the raw route param,
// which may be a slug — unlike every other check-scoped query on this page
// (`/results`, `/checks/{checkUid}`, `/checks/{checkUid}/availability`,
// `/checks/{checkUid}/dependencies`), the incidents endpoint forwarded that
// value straight into a Postgres `uuid` column filter, 500ing with
// "invalid input syntax for type uuid" for any slug-addressed check. The
// fix drives `useIncidents` off `check.uid` (from the already-resolved
// `useCheck` result) instead of the raw param, so the wire request always
// carries the UUID — this holds regardless of backend, since it's a
// client-side normalization (the server also gained slug resolution as the
// durable fix, covered by the Go regression tests in
// server/internal/handlers/incidents/list_by_slug_test.go and
// list_by_slug_postgres_test.go).

const API_BASE = (
  process.env.E2E_BASE_URL ?? "http://localhost:4000/dash0/"
).replace(/\/dash0\/?$/, "");

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

interface CreatedCheck {
  uid: string;
  slug: string;
}

async function createCheckWithSlug(
  page: Page,
  token: string,
  name: string,
  slug: string,
): Promise<CreatedCheck> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name,
      slug,
      type: "http",
      config: { url: "https://example.com/check-incidents-slug" },
    },
  });
  expect(resp.status()).toBe(201);
  const body = await resp.json();
  return { uid: body.uid, slug: body.slug };
}

async function deleteCheck(page: Page, token: string, uid: string): Promise<void> {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe("Check detail incidents: slug vs uid", () => {
  test("navigating via the check's slug requests /incidents with the resolved uid, no 500", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const slug = `e2e-incidents-slug-${Date.now()}`;
    const check = await createCheckWithSlug(page, token, `E2E Incidents Slug ${Date.now()}`, slug);

    try {
      const incidentsResponsePromise = page.waitForResponse(
        (resp) =>
          resp.url().includes("/api/v1/orgs/test/incidents") &&
          resp.request().method() === "GET",
      );

      await page.goto(`orgs/test/checks/${check.slug}`);
      await expect(page.getByTestId("check-detail-header")).toBeVisible();

      const incidentsResponse = await incidentsResponsePromise;

      // The exact bug: the raw slug forwarded as checkUid hit a Postgres
      // uuid column and 500ed. Must be 200 regardless of backend.
      expect(incidentsResponse.status()).toBe(200);

      const requestUrl = new URL(incidentsResponse.request().url());
      expect(
        requestUrl.searchParams.get("checkUid"),
        "the outgoing /incidents request must carry the resolved uid, never the raw slug",
      ).toBe(check.uid);
    } finally {
      await deleteCheck(page, token, check.uid);
    }
  });
});
