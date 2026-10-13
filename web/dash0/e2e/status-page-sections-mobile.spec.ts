import { test, expect, API_BASE } from "./fixtures";
import type { Page } from "@playwright/test";

// fclairamb/solidping-business#9: on a 360px phone the status page editor's
// resource rows let their badges spill past the section card, and the section
// header squeezed its title next to the action icons. A row must keep every
// child inside its own box.

async function post(
  page: Page,
  token: string,
  path: string,
  data: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const resp = await page.request.post(`${API_BASE}${path}`, {
    headers: { Authorization: `Bearer ${token}` },
    data,
  });
  expect(resp.status(), `POST ${path} -> ${await resp.text()}`).toBeLessThan(300);
  return resp.json();
}

test("status page section rows stay inside their card at 360px", async ({
  authenticatedPage,
}) => {
  const page = authenticatedPage;
  await page.setViewportSize({ width: 360, height: 740 });
  const login = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const token = (await login.json()).accessToken as string;
  const suffix = Date.now().toString().slice(-9);

  const check = await post(page, token, "/api/v1/orgs/test/checks", {
    type: "http",
    name: `Legacy billing API ${suffix}`,
    config: { url: "https://example.com" },
    period: "00:05:00",
  });
  const statusPage = await post(page, token, "/api/v1/orgs/test/status-pages", {
    name: `E2E Mobile ${suffix}`,
    slug: `e2e-mob-${suffix}`,
    visibility: "public",
  });
  const section = await post(
    page,
    token,
    `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections`,
    { name: "Everything", slug: "everything" },
  );
  await post(
    page,
    token,
    `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections/${section.uid}/resources`,
    { checkUid: check.uid },
  );

  await page.goto(`orgs/test/status-pages/${statusPage.uid}`);
  const name = page.getByTestId("resource-row-name").first();
  await expect(name).toBeVisible();

  const escaping = await page.evaluate(() => {
    const row = document.querySelector('[data-testid="resource-row-name"]')?.parentElement;
    if (!row) return ["no row"];
    const box = row.getBoundingClientRect();
    return Array.from(row.children)
      .filter((c) => c.getBoundingClientRect().right > box.right + 1)
      .map((c) => c.outerHTML.slice(0, 80));
  });
  expect(escaping).toEqual([]);
});
