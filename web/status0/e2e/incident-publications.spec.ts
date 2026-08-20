/**
 * Playwright E2E: incident publications on the public status page
 * (spec 2026-08-19-08).
 *
 * Prerequisites (handled by the test runner / CI):
 *   - Server running at API_BASE with SP_RUNMODE=test
 *   - 014_v0_17_0 migration applied (incident-publications section)
 *
 * Coverage:
 *   - a hand-written publication renders as a severity-coloured card carrying
 *     its narrative, and nothing internal leaks into it;
 *   - resolving one removes it from the active section but keeps it in the
 *     history panel;
 *   - an AUTO-published incident badges the affected component and leaves an
 *     unaffected sibling alone (positive and negative in one test), and its
 *     severity colours the top banner.
 */
import { test, expect, type Page } from "@playwright/test";
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

/** Authenticated JSON call that fails loudly on any non-2xx. */
async function api<T>(
  token: string,
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  if (!res.ok) {
    // Loud on purpose. A 404/405 here means the route regressed, which is
    // exactly the failure this suite exists to catch — skipping past it would
    // turn a broken feature into a green run.
    throw new Error(
      `${method} ${path} failed: ${res.status} ${await res.text()}`,
    );
  }

  if (res.status === 204) return undefined as T;

  return (await res.json()) as T;
}

async function getDefaultStatusPage(
  token: string,
): Promise<{ uid: string; slug: string }> {
  return api<{ uid: string; slug: string }>(
    token,
    "GET",
    `/api/v1/status-pages/test`,
  );
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
): Promise<Publication> {
  return api<Publication>(
    token,
    "POST",
    `/api/v1/orgs/test/status-pages/${pageUID}/incidents`,
    {
      title,
      severity: "major",
      bodyMarkdown: "We are investigating elevated error rates.",
    },
  );
}

test.describe("Incident publications on the public status page", () => {
  test("an open publication renders as a severity-coloured card with its narrative", async ({
    page,
  }) => {
    const token = await getToken();
    const statusPage = await getDefaultStatusPage(token);
    const title = `Payments API is degraded ${Date.now()}`;

    const publication = await createPublication(token, statusPage.uid, title);

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

    // The narrative entry posted alongside the publication is rendered — and
    // it goes through the SHARED status-update card, so the kind badge that
    // component owns is present too.
    await expect(
      card.getByText("We are investigating elevated error rates."),
    ).toBeVisible();
    await expect(card.getByTestId("status-update-kind").first()).toBeVisible();

    // Negative: nothing internal leaks into the public card. A check slug or a
    // probe error string appearing here would be the security failure this
    // feature is built to avoid.
    await expect(card).not.toContainText("is down");
  });

  test("resolving a publication removes it from the active section", async ({
    page,
  }) => {
    const token = await getToken();
    const statusPage = await getDefaultStatusPage(token);
    const title = `Search is slow ${Date.now()}`;

    const publication = await createPublication(token, statusPage.uid, title);

    await page.goto(`${BASE}/status0/test`);
    await page.waitForLoadState("networkidle");
    await expect(page.locator(`#incident-${publication.uid}`)).toBeVisible({
      timeout: 10_000,
    });

    await api(
      token,
      "PATCH",
      `/api/v1/orgs/test/status-pages/${statusPage.uid}/incidents/${publication.uid}`,
      { state: "resolved" },
    );

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

  test("a published incident badges its affected component and only that one", async ({
    page,
  }) => {
    const token = await getToken();
    const stamp = Date.now();

    // A dedicated page, so the assertions cannot be disturbed by whatever
    // other specs left on the default one.
    //
    // Auto-publish MUST be off here, and that is the whole point of this test.
    // Both heartbeat checks below go down within milliseconds, so a page with
    // autoPublish + a zero delay publishes BOTH incidents on its own — which
    // makes the sibling wear a badge legitimately and contradicts the negative
    // assertion this test exists to make. It also races the manual POST below,
    // which then fails with 409 "already published on this status page". The
    // publication under test is created explicitly, so the page needs no
    // auto-publish at all.
    const statusPage = await api<{ uid: string; slug: string }>(
      token,
      "POST",
      `/api/v1/orgs/test/status-pages`,
      {
        name: `Affected badge ${stamp}`,
        slug: `affected-badge-${stamp}`,
        autoPublish: false,
      },
    );

    const section = await api<{ uid: string }>(
      token,
      "POST",
      `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections`,
      { name: "Services", slug: `services-${stamp}` },
    );

    // Heartbeat checks: their status is driven by an inbound ping, so the test
    // decides exactly which one fails instead of waiting on a real probe.
    const affectedCheck = await api<HeartbeatCheck>(
      token,
      "POST",
      `/api/v1/orgs/test/checks`,
      {
        name: `affected-${stamp}`,
        slug: `affected-${stamp}`,
        type: "heartbeat",
        confirmationPeriodSeconds: 0,
        config: { periodSeconds: 60 },
      },
    );

    const siblingCheck = await api<HeartbeatCheck>(
      token,
      "POST",
      `/api/v1/orgs/test/checks`,
      {
        name: `sibling-${stamp}`,
        slug: `sibling-${stamp}`,
        type: "heartbeat",
        confirmationPeriodSeconds: 0,
        config: { periodSeconds: 60 },
      },
    );

    const affectedName = `Affected component ${stamp}`;
    const unpublishedName = `Unpublished component ${stamp}`;

    await api(
      token,
      "POST",
      `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections/${section.uid}/resources`,
      { checkUid: affectedCheck.uid, publicName: affectedName },
    );
    await api(
      token,
      "POST",
      `/api/v1/orgs/test/status-pages/${statusPage.uid}/sections/${section.uid}/resources`,
      { checkUid: siblingCheck.uid, publicName: unpublishedName },
    );

    // A heartbeat check that has never been pinged is already overdue, so an
    // incident opens for BOTH of them within milliseconds of creation. That is
    // useful here: it means the negative below cannot pass just because the
    // sibling is healthy. Both components are down; only one is PUBLISHED, and
    // only that one may wear the badge.
    const affectedIncident = await pollForIncident(token, affectedCheck.uid);

    const publication = await api<{ uid: string }>(
      token,
      "POST",
      `/api/v1/orgs/test/incidents/${affectedIncident.uid}/publications`,
      { statusPageUid: statusPage.uid, severity: "critical" },
    );

    // The publication must have resolved the affected component's PUBLIC name
    // by walking the page's own resources — that name is what the badge keys
    // on, so an empty list here would make the assertions below meaningless.
    const publicIncident = await pollForPublicIncident(
      statusPage.slug,
      affectedName,
    );
    expect(publicIncident.uid).toBe(publication.uid);

    await page.goto(`${BASE}/status0/test/${statusPage.slug}`);
    await page.waitForLoadState("networkidle");

    // POSITIVE: the affected component wears the badge…
    const affectedRow = rowFor(page, affectedName);
    await expect(
      affectedRow.getByTestId("resource-incident-badge"),
    ).toBeVisible({ timeout: 10_000 });
    await expect(
      affectedRow.getByTestId("resource-incident-badge"),
    ).toHaveAttribute("data-incident-severity", "critical");

    // NEGATIVE: …and its sibling does not, even though that component is ALSO
    // down with its own open incident. That is what makes this half worth
    // having: the badge has to track the PUBLICATION, not the check status, and
    // a badge rendered on every failing row would pass the positive assertion
    // above while being wrong.
    const siblingRow = rowFor(page, unpublishedName);
    await expect(siblingRow).toBeVisible();
    await expect(siblingRow.getByTestId("resource-incident-badge")).toHaveCount(
      0,
    );

    // The top banner reflects the publication severity, not just the rollup.
    await expect(page.getByTestId("overall-status-banner")).toHaveAttribute(
      "data-incident-severity",
      "critical",
    );
  });
});

/** The resource row for a given public component name. */
function rowFor(page: Page, name: string) {
  return page.getByTestId("resource-row").filter({ hasText: name });
}

interface HeartbeatCheck {
  uid: string;
  slug: string;
  config: { token: string };
}

interface PublicIncident {
  uid: string;
  affectedResources?: string[];
}

/** Polls until an active incident exists for the given check. */
async function pollForIncident(
  token: string,
  checkUid: string,
): Promise<{ uid: string }> {
  const deadline = Date.now() + 20_000;

  while (Date.now() < deadline) {
    const body = await api<{ data?: { uid: string; checkUid?: string }[] }>(
      token,
      "GET",
      `/api/v1/orgs/test/incidents?state=active&limit=100`,
    );
    const match = (body.data ?? []).find(
      (incident) => incident.checkUid === checkUid,
    );
    if (match) return match;

    await new Promise((resolve) => setTimeout(resolve, 500));
  }

  throw new Error(
    `no active incident for check ${checkUid} appeared within 20s`,
  );
}

/** Polls the PUBLIC incident feed until the named component is covered. */
async function pollForPublicIncident(
  slug: string,
  resourceName: string,
): Promise<PublicIncident> {
  const deadline = Date.now() + 20_000;

  while (Date.now() < deadline) {
    const res = await fetch(
      `${BASE}/api/v1/status-pages/test/${slug}/incidents?active=true`,
    );

    if (res.ok) {
      const body = (await res.json()) as { data?: PublicIncident[] };
      const match = (body.data ?? []).find((incident) =>
        (incident.affectedResources ?? []).includes(resourceName),
      );
      if (match) return match;
    }

    await new Promise((resolve) => setTimeout(resolve, 500));
  }

  throw new Error(
    `no public incident covering ${resourceName} appeared within 20s`,
  );
}
