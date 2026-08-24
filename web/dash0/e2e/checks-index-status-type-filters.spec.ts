import { test, expect, type Page } from "./fixtures";

// Coverage for spec 2026-08-24-04: the checks-list status filter becomes a
// multi-select FacetedFilter popover (adds validating, multi-status), and a
// new type filter is added alongside it, same component.
//
// Like checks-index-filter-hides-empty-groups.spec.ts, the first three
// scenarios mock /check-groups, /checks, /check-types and
// /escalation-policies directly: a real "down" or "validating" status can
// only be produced by an actual failing probe (or, for validating, a
// disabled fixture — see the last test below), so mocking keeps the
// toggle/URL/negative-control assertions deterministic and fast.

interface MockCheck {
  uid: string;
  name: string;
  status: string;
  type: string;
}

async function mockChecksIndex(
  page: Page,
  opts: { checks: MockCheck[]; checkTypes: string[] },
): Promise<void> {
  const groupUid = "e2e-status-type-group";

  await page.route("**/api/v1/orgs/*/check-groups*", (route) => {
    const url = route.request().url();
    if (!url.includes("/check-groups")) return route.continue();
    const counts: Record<string, number> = {};
    for (const c of opts.checks) counts[c.status] = (counts[c.status] ?? 0) + 1;
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: [
          {
            uid: groupUid,
            name: "E2E Status Type Group",
            slug: groupUid,
            sortOrder: 0,
            checkCount: opts.checks.length,
            status: "down",
            memberStatusCounts: counts,
            escalationPolicyUid: null,
            createdAt: new Date().toISOString(),
            updatedAt: new Date().toISOString(),
          },
        ],
      }),
    });
  });

  // Server-filtered, mirroring the real ?status=/?type= comma-separated IN
  // semantics: both dimensions narrow (AND), each is a union within itself
  // (OR) — e.g. status=down,validating&type=http keeps a check that is
  // (down OR validating) AND http.
  await page.route("**/api/v1/orgs/*/checks*", (route) => {
    const url = route.request().url();
    if (!url.includes("/checks") || url.includes("/check-groups") || url.includes("/check-types")) {
      return route.continue();
    }
    const params = new URL(url).searchParams;
    const statusFilter = params.get("status")?.split(",").filter(Boolean);
    const typeFilter = params.get("type")?.split(",").filter(Boolean);
    const filtered = opts.checks.filter((c) => {
      if (statusFilter && !statusFilter.includes(c.status)) return false;
      if (typeFilter && !typeFilter.includes(c.type)) return false;
      return true;
    });
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: filtered.map((c) => ({
          uid: c.uid,
          name: c.name,
          slug: c.uid,
          type: c.type,
          enabled: true,
          checkGroupUid: groupUid,
          status: c.status,
          lastResult: { status: c.status, durationMs: 42 },
          config: { url: `https://example.com/${c.uid}` },
        })),
        // No cursor: a single page, so filtering resolves immediately.
        pagination: { total: filtered.length },
      }),
    });
  });

  await page.route("**/api/v1/orgs/*/check-types*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        data: opts.checkTypes.map((t) => ({
          type: t,
          description: t,
          labels: [],
          enabled: true,
        })),
      }),
    }),
  );

  await page.route("**/api/v1/orgs/*/escalation-policies*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: [] }),
    }),
  );
}

test.describe("Checks list — status faceted filter", () => {
  test("toggling two statuses narrows the list, updates the URL, and excludes a third status", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const checks: MockCheck[] = [
      { uid: "e2e-st-up", name: "ST Up Check", status: "up", type: "http" },
      { uid: "e2e-st-down", name: "ST Down Check", status: "down", type: "http" },
      { uid: "e2e-st-validating", name: "ST Validating Check", status: "validating", type: "http" },
    ];
    await mockChecksIndex(page, { checks, checkTypes: ["http"] });
    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("ST Up Check")).toBeVisible();
    await expect(page.getByText("ST Down Check")).toBeVisible();
    await expect(page.getByText("ST Validating Check")).toBeVisible();
    expect(new URL(page.url()).searchParams.has("status")).toBe(false);

    await page.getByTestId("status-filter").click();
    await page.getByTestId("status-filter-option-down").click();
    await page.getByTestId("status-filter-option-validating").click();
    await page.keyboard.press("Escape");
    await page.waitForLoadState("networkidle");

    await expect
      .poll(() => new URL(page.url()).searchParams.get("status")?.split(",").sort())
      .toEqual(["down", "validating"]);

    await expect(page.getByText("ST Down Check")).toBeVisible();
    await expect(page.getByText("ST Validating Check")).toBeVisible();
    // Negative control: the up check sits in a third status, absent from the
    // narrowed list — not just "fewer rows".
    await expect(page.getByText("ST Up Check")).not.toBeVisible();

    await expect(page.getByTestId("status-filter")).toContainText("Down +1");
  });

  test("cold deep link with two statuses shows both selected on first paint and filters the list", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const checks: MockCheck[] = [
      { uid: "e2e-cold-up", name: "Cold Up Check", status: "up", type: "http" },
      { uid: "e2e-cold-down", name: "Cold Down Check", status: "down", type: "http" },
      { uid: "e2e-cold-validating", name: "Cold Validating Check", status: "validating", type: "http" },
    ];
    await mockChecksIndex(page, { checks, checkTypes: ["http"] });

    // A genuine cold load of the deep-linked URL (not client-side nav): the
    // URL is the only source of the selection, read straight off
    // Route.useSearch() with no local-state mirror.
    await page.goto("orgs/test/checks?status=down,validating");
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Cold Down Check")).toBeVisible();
    await expect(page.getByText("Cold Validating Check")).toBeVisible();
    await expect(page.getByText("Cold Up Check")).not.toBeVisible();

    await expect(page.getByTestId("status-filter")).toContainText("Down +1");
    await page.getByTestId("status-filter").click();
    await expect(page.getByTestId("status-filter-option-down").getByRole("checkbox")).toBeChecked();
    await expect(page.getByTestId("status-filter-option-validating").getByRole("checkbox")).toBeChecked();
    await expect(page.getByTestId("status-filter-option-up").getByRole("checkbox")).not.toBeChecked();
  });
});

test.describe("Checks list — type faceted filter", () => {
  test("type filter alone, and combined with a status filter, neither clears the other", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const checks: MockCheck[] = [
      { uid: "e2e-ty-http-up", name: "Type HTTP Up", status: "up", type: "http" },
      { uid: "e2e-ty-http-down", name: "Type HTTP Down", status: "down", type: "http" },
      { uid: "e2e-ty-tcp-up", name: "Type TCP Up", status: "up", type: "tcp" },
    ];
    await mockChecksIndex(page, { checks, checkTypes: ["http", "tcp"] });
    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");

    // Type filter alone narrows to the two http checks, hiding the tcp one.
    await page.getByTestId("type-filter").click();
    await page.getByTestId("type-filter-option-http").click();
    await page.keyboard.press("Escape");
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Type HTTP Up")).toBeVisible();
    await expect(page.getByText("Type HTTP Down")).toBeVisible();
    await expect(page.getByText("Type TCP Up")).not.toBeVisible();
    expect(new URL(page.url()).searchParams.get("type")).toBe("http");

    // Combine with a status filter — narrows further, without clearing type.
    await page.getByTestId("status-filter").click();
    await page.getByTestId("status-filter-option-down").click();
    await page.keyboard.press("Escape");
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Type HTTP Down")).toBeVisible();
    await expect(page.getByText("Type HTTP Up")).not.toBeVisible();
    await expect(page.getByText("Type TCP Up")).not.toBeVisible();
    expect(new URL(page.url()).searchParams.get("type")).toBe("http");
    expect(new URL(page.url()).searchParams.get("status")).toBe("down");

    // Clearing the status filter restores the type-only view — type survives
    // the status writer, same functional (prev) => ({...prev, ...}) contract
    // the q/labels/groupBy writers already use.
    await page.getByTestId("status-filter").click();
    await page.getByTestId("status-filter-option-down").click();
    await page.keyboard.press("Escape");
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Type HTTP Up")).toBeVisible();
    await expect(page.getByText("Type HTTP Down")).toBeVisible();
    await expect(page.getByText("Type TCP Up")).not.toBeVisible();
    expect(new URL(page.url()).searchParams.get("type")).toBe("http");
    expect(new URL(page.url()).searchParams.has("status")).toBe(false);
  });
});

test.describe("Checks list — validating status against the real backend", () => {
  test("a validating check appears under ?status=validating", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    // Seeded by server/test/testdata/testdata.go's createTestValidatingCheck
    // fixture: a disabled check whose stored status is
    // models.CheckStatusValidating, deterministic without racing the real
    // confirmation-period timer. Proves the backend token end to end (the
    // handler test in server/internal/handlers/checks covers the negative
    // control and the union/unknown-token cases at the API layer).
    await page.goto("orgs/test/checks?status=validating");
    await page.waitForLoadState("networkidle");

    await expect(page.getByText("Validating Check")).toBeVisible();
    await expect(page.getByTestId("status-filter")).toContainText("Validating");
  });
});
