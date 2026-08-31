import { test, expect, API_BASE } from "./fixtures";
import type { Page } from "@playwright/test";

// Coverage for spec 2026-08-29-08: TV mode, the wallboard rendering of a
// status page.
//
// What is proven here is what only a browser can prove — that the board
// renders the right ambient state, that a kiosk token really lets an
// UNAUTHENTICATED screen through a password gate, and that the board stops
// claiming to be green when the data stops arriving. The arithmetic behind the
// uptime number and the whole kiosk security matrix (wrong token, revoked
// token, regenerate, no-oracle) live in the Go tests, where the negatives can
// be asserted against a real database.

const OPERATIONAL = "All Systems Operational";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });

  return (await resp.json()).accessToken;
}

/** Creates a status page through the API and returns { uid, slug }. */
async function createPage(
  page: Page,
  token: string,
  suffix: string,
  extra: Record<string, unknown> = {},
): Promise<{ uid: string; slug: string }> {
  const slug = `e2e-tv-${suffix}`.slice(0, 40);
  const resp = await page.request.post(
    `${API_BASE}/api/v1/orgs/test/status-pages`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { name: `E2E TV ${suffix}`, slug, ...extra },
    },
  );

  expect(
    resp.status(),
    `create status page -> ${await resp.text()}`,
  ).toBeLessThan(300);

  return { uid: (await resp.json()).uid, slug };
}

async function deletePage(page: Page, token: string, uid: string) {
  await page.request.delete(
    `${API_BASE}/api/v1/orgs/test/status-pages/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
}

test.describe("Status page TV mode", () => {
  test.describe.configure({ timeout: 120_000 });

  test("renders the operational board with the page-level uptime number", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);
    const { uid, slug } = await createPage(page, token, suffix);

    try {
      // A freshly created page has no probe history, so the server has no
      // uptime to publish and correctly omits the field. The board's rendering
      // of that number is what this test is about, so the payload is stubbed
      // with a page that HAS data — the arithmetic producing it is covered in
      // server/internal/handlers/statuspages/page_availability_test.go.
      await page.route(`**/api/v1/status-pages/test/${slug}`, async (route) => {
        const response = await route.fetch();
        const body = await response.json();

        await route.fulfill({
          response,
          json: {
            ...body,
            overallStatus: "operational",
            historyPeriod: "30d",
            overallAvailabilityPct: 99.87,
          },
        });
      });

      await page.goto(`/status0/test/${slug}/tv`);

      const board = page.getByTestId("tv-board");
      await expect(board).toBeVisible({ timeout: 30000 });
      await expect(board).toHaveAttribute("data-tv-state", "operational");

      // Colour is never the only signal: the state is spelled out, and an icon
      // sits beside it.
      await expect(page.getByTestId("tv-headline")).toHaveText(OPERATIONAL);
      await expect(page.getByTestId("tv-state-icon")).toBeVisible();

      const availability = page.getByTestId("tv-availability");
      await expect(availability).toBeVisible();
      await expect(availability).toContainText("99.87%");
      // Labelled with the window it covers — a bare percentage on a wall is
      // a percentage of nothing.
      await expect(availability).toContainText("30-day uptime");

      // No incident has ever been recorded on a brand-new page.
      await expect(page.getByTestId("tv-days-since")).toContainText(
        "No incidents recorded",
      );

      // The board is one viewport: it must not scroll.
      const scrolls = await page.evaluate(
        () => document.documentElement.scrollHeight > window.innerHeight + 4,
      );
      expect(scrolls, "the TV board must fit one viewport").toBe(false);
    } finally {
      await deletePage(page, token, uid);
    }
  });

  // /{org}/tv addresses the org's DEFAULT page — the shape an operator reaches
  // for first ("point the TV at us"). It has its own route file, so nothing
  // about the slug route proves it works.
  test("the org-level URL renders the default page's board", async ({
    page,
  }) => {
    await page.goto(`/status0/test/tv`);

    const board = page.getByTestId("tv-board");
    await expect(board).toBeVisible({ timeout: 30000 });
    // The seeded default page, resolved without naming it in the URL.
    await expect(page.getByTestId("tv-page-name")).toHaveText(
      "Test Status Page",
    );
  });

  test("an active incident flips the ambient state and shows a ticking duration", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);
    const { uid, slug } = await createPage(page, token, suffix);

    try {
      // Published by hand while every probe is green — the case the board must
      // never swallow. A manually published critical incident has to turn the
      // wall red even though nothing is failing a check.
      const title = `E2E TV incident ${suffix}`;
      const created = await page.request.post(
        `${API_BASE}/api/v1/orgs/test/status-pages/${uid}/incidents`,
        {
          headers: { Authorization: `Bearer ${token}` },
          data: { title, severity: "critical", state: "investigating" },
        },
      );
      expect(
        created.status(),
        `publish incident -> ${await created.text()}`,
      ).toBeLessThan(300);

      await page.goto(`/status0/test/${slug}/tv`);

      const board = page.getByTestId("tv-board");
      await expect(board).toBeVisible({ timeout: 30000 });
      await expect(board).toHaveAttribute("data-tv-state", "down");
      await expect(page.getByTestId("tv-headline")).not.toHaveText(OPERATIONAL);

      const incident = page.getByTestId("tv-active-incident").first();
      await expect(incident).toBeVisible();
      await expect(
        page.getByTestId("tv-active-incident-title").first(),
      ).toHaveText(title);
      await expect(
        page.getByTestId("tv-active-incident-severity").first(),
      ).toHaveText(/critical/i);
      await expect(
        page.getByTestId("tv-active-incident-duration").first(),
      ).toContainText("ongoing for");

      // "Days since the last incident" is a contradiction next to a live one.
      await expect(page.getByTestId("tv-days-since")).toHaveCount(0);
    } finally {
      await deletePage(page, token, uid);
    }
  });

  test("a kiosk token lets an unauthenticated screen past the password gate", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);
    const { uid, slug } = await createPage(page, token, suffix, {
      visibility: "password",
      password: "correct-horse",
    });

    try {
      const minted = await page.request.post(
        `${API_BASE}/api/v1/orgs/test/status-pages/${uid}/kiosk-token`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      expect(
        minted.status(),
        `mint kiosk token -> ${await minted.text()}`,
      ).toBe(201);

      const kiosk: string = (await minted.json()).token;
      expect(kiosk).toBeTruthy();

      // A brand-new browser context: no dashboard session, no unlock cookie.
      // This is the wallboard, and it must get in on the token alone.
      const screen = await page.context().browser()!.newContext();
      const tv = await screen.newPage();

      try {
        // Negative control FIRST, on the same fresh context: without the
        // token the very same URL must not render a board.
        await tv.goto(`${API_BASE}/status0/test/${slug}/tv`);
        await expect(tv.getByTestId("tv-locked")).toBeVisible({
          timeout: 30000,
        });
        await expect(tv.getByTestId("tv-board")).toHaveCount(0);

        await tv.goto(
          `${API_BASE}/status0/test/${slug}/tv?kiosk=${encodeURIComponent(kiosk)}`,
        );

        await expect(tv.getByTestId("tv-board")).toBeVisible({
          timeout: 30000,
        });
        // No password field anywhere: nobody is standing at the screen.
        await expect(tv.locator('input[type="password"]')).toHaveCount(0);

        // The token is erased from the address bar so it is not left legible
        // on a wall for months — but the board keeps polling with it.
        await expect
          .poll(() => new URL(tv.url()).searchParams.get("kiosk"), {
            timeout: 10000,
          })
          .toBeNull();
        await expect(tv.getByTestId("tv-board")).toBeVisible();
      } finally {
        await screen.close();
      }
    } finally {
      await deletePage(page, token, uid);
    }
  });

  test("the board turns grey when the data stops arriving", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);
    const { uid, slug } = await createPage(page, token, suffix);

    try {
      // A fake clock, so 90 s of silence takes milliseconds. Installed before
      // navigation so React Query's timers and the board's own staleness
      // interval are both on it.
      await page.clock.install();

      let firstLoadDone = false;
      await page.route(
        `**/api/v1/status-pages/test/${slug}**`,
        async (route) => {
          if (firstLoadDone) {
            // Every poll after the first one fails, exactly as it would with a
            // dead uplink.
            await route.abort("failed");

            return;
          }

          firstLoadDone = true;
          await route.continue();
        },
      );

      await page.goto(`/status0/test/${slug}/tv`);

      const board = page.getByTestId("tv-board");
      await expect(board).toBeVisible({ timeout: 30000 });
      // Positive control: it really was showing a confident state before the
      // network died, so the assertion below is proving the guard rather than
      // a board that never rendered.
      await expect(board).not.toHaveAttribute("data-tv-state", "stale");

      // Three missed 30 s polls, plus slack.
      await page.clock.fastForward(120_000);

      await expect(board).toHaveAttribute("data-tv-state", "stale", {
        timeout: 15000,
      });
      await expect(page.getByTestId("tv-headline")).toHaveText(
        "Data Unavailable",
      );
      await expect(page.getByTestId("tv-stale-notice")).toContainText(
        "No update received since",
      );
      // Never leave a confident green up over data that is dead.
      await expect(page.getByTestId("tv-headline")).not.toHaveText(OPERATIONAL);
    } finally {
      await deletePage(page, token, uid);
    }
  });

  test("the dashboard offers the TV URL, and a kiosk control only when the page is gated", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const suffix = Date.now().toString().slice(-9);
    const publicPage = await createPage(page, token, `${suffix}a`);
    const gated = await createPage(page, token, `${suffix}b`, {
      visibility: "password",
      password: "correct-horse",
    });

    try {
      await page.goto(`orgs/test/status-pages/${publicPage.uid}`);

      const card = page.getByTestId("status-page-tv-card");
      await expect(card).toBeVisible({ timeout: 30000 });
      await expect(page.getByTestId("tv-mode-url")).toContainText(
        `/status0/test/${publicPage.slug}/tv`,
      );
      // A public page needs no token, so it is offered no token control.
      await expect(page.getByTestId("tv-mode-public-note")).toBeVisible();
      await expect(page.getByTestId("tv-mode-generate")).toHaveCount(0);

      await page.goto(`orgs/test/status-pages/${gated.uid}`);
      await expect(page.getByTestId("status-page-tv-card")).toBeVisible({
        timeout: 30000,
      });
      await expect(page.getByTestId("tv-mode-restricted-note")).toBeVisible();

      const generate = page.getByTestId("tv-mode-generate");
      await expect(generate).toBeVisible();
      await generate.click();

      // Shown once, and the copyable URL switches to the tokened one — copying
      // the bare URL here and finding it 401s on the TV is the trap the card
      // exists to close.
      const alert = page.getByTestId("tv-mode-token-alert");
      await expect(alert).toBeVisible({ timeout: 30000 });
      await expect(page.getByTestId("tv-mode-url")).toContainText("?kiosk=");

      // Once a token exists the affordance becomes regenerate + revoke.
      await expect(page.getByTestId("tv-mode-regenerate")).toBeVisible();
      await expect(page.getByTestId("tv-mode-revoke")).toBeVisible();
    } finally {
      await deletePage(page, token, publicPage.uid);
      await deletePage(page, token, gated.uid);
    }
  });
});
