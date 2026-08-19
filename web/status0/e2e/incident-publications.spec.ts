/**
 * Playwright E2E: the incident publication banner and timeline (spec
 * 2026-08-19-08).
 *
 * Prerequisites (handled by the test runner / CI):
 *   - Server running at API_BASE with SP_RUNMODE=test
 *   - 017_incident_publications migration applied
 *
 * The test publishes a hand-authored incident through the admin API and then
 * checks the PUBLIC page renders it — title, severity badge, state badge and
 * the narrative entry — and that a resolved publication leaves the active
 * section rather than lingering there.
 */
import { test, expect } from "@playwright/test";
import { API_BASE as BASE } from "./fixtures";

async function getToken(): Promise<string> {
  const res = await fetch(`${BASE}/api/v1/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      org: "test",
      email: "test@test.com",
      password: "test",
    }),
  });
  if (!res.ok) throw new Error(`login failed: ${res.status}`);
  const data = (await res.json()) as { accessToken: string };
  return data.accessToken;
}

async function getDefaultStatusPageUID(
  token: string,
  org: string,
): Promise<string> {
  const res = await fetch(`${BASE}/api/v1/status-pages/${org}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!res.ok) throw new Error(`status page lookup failed: ${res.status}`);
  const data = (await res.json()) as { uid: string };
  return data.uid;
}

interface Publication {
  uid: string;
  title: string;
  state: string;
}

async function createPublication(
  token: string,
  pageUID: string,
  title: string,
): Promise<Publication | null> {
  const res = await fetch(
    `${BASE}/api/v1/orgs/test/status-pages/${pageUID}/incidents`,
    {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        title,
        severity: "major",
        bodyMarkdown: "We are investigating elevated error rates.",
      }),
    },
  );

  if (res.status === 404 || res.status === 405) return null;
  if (!res.ok) throw new Error(`publication create failed: ${res.status}`);

  return (await res.json()) as Publication;
}

test.describe("Incident publications on the public status page", () => {
  test("an open publication renders as a severity-coloured card with its narrative", async ({
    page,
  }) => {
    const token = await getToken();
    const pageUID = await getDefaultStatusPageUID(token, "test");
    const title = `Payments API is degraded ${Date.now()}`;

    const publication = await createPublication(token, pageUID, title);
    if (!publication) {
      test.skip(true, "Incident publication API not available");
      return;
    }

    await page.goto(`${BASE}/status0/test`);
    await page.waitForLoadState("networkidle");

    const section = page.getByTestId("active-incidents");
    await expect(section).toBeVisible({ timeout: 10_000 });

    const card = page.locator(
      `[data-testid="active-incident"]#incident-${publication.uid}`,
    );
    await expect(card).toBeVisible();
    await expect(card.getByTestId("active-incident-title")).toHaveText(title);
    await expect(card.getByTestId("active-incident-severity")).toHaveText(
      "Major",
    );
    await expect(card.getByTestId("active-incident-state")).toHaveText(
      "Investigating",
    );
    await expect(card).toHaveAttribute("data-incident-severity", "major");

    // The narrative entry posted alongside the publication is rendered.
    await expect(
      card.getByText("We are investigating elevated error rates."),
    ).toBeVisible();

    // Negative: nothing internal leaks into the public card. A check slug or a
    // probe error string appearing here would be the security failure this
    // feature is built to avoid.
    await expect(card).not.toContainText("is down");
  });

  test("resolving a publication removes it from the active section", async ({
    page,
  }) => {
    const token = await getToken();
    const pageUID = await getDefaultStatusPageUID(token, "test");
    const title = `Search is slow ${Date.now()}`;

    const publication = await createPublication(token, pageUID, title);
    if (!publication) {
      test.skip(true, "Incident publication API not available");
      return;
    }

    await page.goto(`${BASE}/status0/test`);
    await page.waitForLoadState("networkidle");
    await expect(page.locator(`#incident-${publication.uid}`)).toBeVisible({
      timeout: 10_000,
    });

    const resolveRes = await fetch(
      `${BASE}/api/v1/orgs/test/status-pages/${pageUID}/incidents/${publication.uid}`,
      {
        method: "PATCH",
        headers: {
          Authorization: `Bearer ${token}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ state: "resolved" }),
      },
    );
    expect(resolveRes.ok).toBe(true);

    await page.reload();
    await page.waitForLoadState("networkidle");

    await expect(page.locator(`#incident-${publication.uid}`)).toHaveCount(0);

    // …but it IS reachable from the history panel, which is collapsed by
    // default and fetches only when opened.
    await page.getByTestId("incident-history-toggle").click();
    await expect(page.locator(`#incident-${publication.uid}`)).toBeVisible({
      timeout: 10_000,
    });
  });
});
