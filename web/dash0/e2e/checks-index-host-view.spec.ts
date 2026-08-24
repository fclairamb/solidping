import { test, expect, type Page } from "./fixtures";

// Coverage for spec 2026-08-01-04: the checks index gains a "Group by:
// Groups / Host" toggle. In Host mode, checks are bucketed client-side by
// their derived targetHost field (server-computed, see checkerdef.
// ExtractTargetHost) — checks with no derivable targetHost (heartbeat/email)
// land in a trailing "No host" section.
//
// The check-groups list is mocked empty (irrelevant to Host mode) and the
// paginated checks endpoint is mocked directly with a fixed, pre-ordered
// fixture — this is a UI-bucketing test, not a backend-sort test (that's
// covered by the Go table tests in server/internal/db/{postgres,sqlite}).

interface MockCheck {
  uid: string;
  name: string;
  type: string;
  status: string;
  targetHost: string | null;
  /** Omit (or pass null) to simulate a check that has never produced a
   * timing — see spec 2026-08-24-12: the empty-duration cell must render
   * a literal em dash, not the escape sequence. */
  durationMs?: number | null;
}

function hostViewFixture(): MockCheck[] {
  return [
    { uid: "e2e-host-tcp", name: "TCP Node", type: "tcp", status: "up", targetHost: "shared.example.com" },
    { uid: "e2e-host-http", name: "HTTP Node", type: "http", status: "up", targetHost: "shared.example.com" },
    { uid: "e2e-host-ssl", name: "SSL Node", type: "ssl", status: "up", targetHost: "shared.example.com" },
    { uid: "e2e-host-heartbeat", name: "Heartbeat Node", type: "heartbeat", status: "up", targetHost: null },
  ];
}

async function mockChecksIndex(page: Page, checks: MockCheck[]): Promise<void> {
  await page.route("**/api/v1/orgs/*/check-groups*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    }),
  );

  await page.route("**/api/v1/orgs/*/checks*", (route) => {
    const url = route.request().url();
    if (!url.includes("/checks") || url.includes("/check-groups")) {
      return route.continue();
    }
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: checks.map((c) => ({
          uid: c.uid,
          name: c.name,
          slug: c.uid,
          type: c.type,
          enabled: true,
          checkGroupUid: null,
          targetHost: c.targetHost,
          status: c.status,
          lastResult: { status: c.status, durationMs: c.durationMs === undefined ? 12 : c.durationMs },
          config:
            c.type === "heartbeat"
              ? { token: "tok" }
              : { host: c.targetHost, url: `https://${c.targetHost}/` },
        })),
        pagination: { total: checks.length },
      }),
    });
  });

  await page.route("**/api/v1/orgs/*/escalation-policies*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    }),
  );
}

test.describe("Checks index host view", () => {
  test("toggling to Host view groups the tcp/http/ssl checks of one host under a single header", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockChecksIndex(page, hostViewFixture());

    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    // Default is Groups mode — the host view isn't rendered yet.
    await expect(page.getByTestId("group-by-groups")).toHaveAttribute("aria-pressed", "true");
    await expect(page.getByTestId("host-view")).not.toBeVisible();

    await page.getByTestId("group-by-host").click();
    await expect(page).toHaveURL(/groupBy=host/);
    await expect(page.getByTestId("group-by-host")).toHaveAttribute("aria-pressed", "true");

    const hostView = page.getByTestId("host-view");
    await expect(hostView).toBeVisible({ timeout: 10000 });

    const hostSection = hostView
      .getByTestId("host-section")
      .filter({ has: page.getByTestId("host-section-name").getByText("shared.example.com") });
    await expect(hostSection).toBeVisible();

    // Every fixture check is "up", so the section defaults to collapsed
    // (same rule as CheckGroupSection) — expand it to see the member rows.
    await hostSection.getByTestId("host-section-header").click();

    // All three of the shared host's checks appear under its single header —
    // the whole point of the by-host view.
    await expect(hostSection.getByRole("link", { name: "TCP Node", exact: true })).toBeVisible();
    await expect(hostSection.getByRole("link", { name: "HTTP Node", exact: true })).toBeVisible();
    await expect(hostSection.getByRole("link", { name: "SSL Node", exact: true })).toBeVisible();

    // The heartbeat check (no derivable targetHost) lands in a trailing
    // "No host" section, separate from the real host's section.
    const noHostSection = hostView
      .getByTestId("host-section")
      .filter({ has: page.getByTestId("host-section-name").getByText("No host") });
    await expect(noHostSection).toBeVisible();
    await noHostSection.getByTestId("host-section-header").click();
    await expect(noHostSection.getByRole("link", { name: "Heartbeat Node", exact: true })).toBeVisible();
  });

  test("deep-linking with groupBy=host renders the Host view directly", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockChecksIndex(page, hostViewFixture());

    await page.goto("orgs/test/checks?groupBy=host");
    await page.waitForLoadState("networkidle");

    // Host view renders on the very first load — no click needed — and the
    // toggle reflects the deep-linked mode.
    await expect(page.getByTestId("group-by-host")).toHaveAttribute("aria-pressed", "true");
    await expect(page.getByTestId("group-by-groups")).toHaveAttribute("aria-pressed", "false");

    const hostView = page.getByTestId("host-view");
    await expect(hostView).toBeVisible({ timeout: 10000 });

    const hostSection = hostView
      .getByTestId("host-section")
      .filter({ has: page.getByTestId("host-section-name").getByText("shared.example.com") });
    await expect(hostSection).toBeVisible();
    await hostSection.getByTestId("host-section-header").click();
    await expect(hostSection.getByRole("link", { name: "TCP Node", exact: true })).toBeVisible();
  });

  // Regression coverage for spec 2026-08-24-12: the duration cell's
  // empty-state text node used to contain the JS escape sequence —
  // instead of a literal em dash. JSX text isn't a JS string literal, so it
  // rendered as those six characters verbatim. Asserting only that a dash
  // is *present* would still pass on the broken string — the assertion that
  // actually proves the fix is that the raw escape sequence is *absent*.
  test("renders a literal em dash, not the escape sequence, when a check has no duration", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockChecksIndex(page, [
      {
        uid: "e2e-no-duration",
        name: "No Duration Node",
        type: "tcp",
        status: "up",
        targetHost: "no-duration.example.com",
        durationMs: null,
      },
    ]);

    await page.goto("orgs/test/checks?groupBy=host");
    await page.waitForLoadState("networkidle");

    const hostView = page.getByTestId("host-view");
    await expect(hostView).toBeVisible({ timeout: 10000 });

    const hostSection = hostView
      .getByTestId("host-section")
      .filter({ has: page.getByTestId("host-section-name").getByText("no-duration.example.com") });
    await expect(hostSection).toBeVisible();
    await hostSection.getByTestId("host-section-header").click();

    const row = page.getByRole("row").filter({ has: page.getByRole("link", { name: "No Duration Node", exact: true }) });
    await expect(row).toBeVisible();

    // The duration column is the 5th cell: Name, Type, Target, Status,
    // Duration, Actions.
    const durationCell = row.getByRole("cell").nth(4);
    await expect(durationCell).toHaveText("—");

    const rowText = (await row.innerText()).trim();
    expect(rowText).not.toContain("\\u2014");
  });
});
