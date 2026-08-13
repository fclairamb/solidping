import { test, expect } from "./fixtures";

// Phase 2 UI of spec 2026-08-12-03: the members page grows a paging-coverage
// column, and any member reachable only by email is badged there and in the
// on-call schedule editor — the two places where an admin is one click away
// from putting that person on the hook.

const COVERAGE = {
  data: [
    {
      userUid: "user-zoe",
      email: "zoe@acme.test",
      name: "Zoe",
      role: "admin",
      channels: [
        { type: "email", verified: true, enabled: true },
        { type: "phone", verified: true, enabled: true },
      ],
      emailFallbackOnly: false,
    },
    {
      userUid: "user-bob",
      email: "bob@acme.test",
      name: "Bob",
      role: "user",
      channels: [
        { type: "email", verified: true, enabled: true },
        // An unverified phone is NOT coverage — the row must still be badged.
        { type: "phone", verified: false, enabled: true },
      ],
      emailFallbackOnly: true,
    },
  ],
};

const MEMBERS = {
  data: [
    {
      uid: "member-zoe",
      userUid: "user-zoe",
      email: "zoe@acme.test",
      name: "Zoe",
      role: "admin",
      createdAt: new Date().toISOString(),
    },
    {
      uid: "member-bob",
      userUid: "user-bob",
      email: "bob@acme.test",
      name: "Bob",
      role: "user",
      createdAt: new Date().toISOString(),
    },
  ],
};

async function stubCoverage(page: import("./fixtures").Page) {
  await page.route("**/api/v1/orgs/test/members/coverage", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(COVERAGE),
    });
  });

  await page.route("**/api/v1/orgs/test/members", async (route) => {
    if (route.request().method() !== "GET") {
      await route.continue();

      return;
    }

    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(MEMBERS),
    });
  });
}

test.describe("Member paging coverage", () => {
  test("members page shows a coverage column and badges email-only members", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubCoverage(page);

    await page.goto("orgs/test/organization/members");
    await page.waitForLoadState("networkidle");

    const bobRow = page.getByRole("row", { name: /bob@acme\.test/ });
    const zoeRow = page.getByRole("row", { name: /zoe@acme\.test/ });

    // Bob has nothing verified beyond email → badged.
    await expect(bobRow.getByTestId("coverage-email-only")).toBeVisible();
    // Zoe has a verified phone → no badge. This is the positive control: the
    // badge must not simply render on every row.
    await expect(zoeRow.getByTestId("coverage-email-only")).toHaveCount(0);

    // Both members still show their channel icons, including the unverified one.
    await expect(
      bobRow.getByTestId("coverage-channel-phone"),
    ).toBeVisible();
  });

  test("members page links to the admin paging setup page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubCoverage(page);

    await page.goto("orgs/test/organization/members");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("member-paging-bob@acme.test").click();
    await page.waitForURL(/\/organization\/members\/member-bob\/paging$/);

    // The page is explicit that admin-added contacts are not pageable yet.
    await expect(page.getByTestId("paging-value")).toBeVisible();
    await expect(page.getByTestId("paging-submit")).toBeVisible();
    await expect(page.getByTestId("paging-nudge")).toBeVisible();
  });

  test("on-call schedule form warns next to email-only roster members", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubCoverage(page);

    await page.goto("orgs/test/on-call/new");
    await page.waitForLoadState("networkidle");

    const bobLabel = page.locator("label", { hasText: "bob@acme.test" });
    await expect(bobLabel.getByTestId("coverage-email-only")).toBeVisible();

    const zoeLabel = page.locator("label", { hasText: "zoe@acme.test" });
    await expect(zoeLabel.getByTestId("coverage-email-only")).toHaveCount(0);
  });

  // The detail page is where an admin reviews an existing rotation, so the
  // email-only signal has to be there too — not only while editing the roster.
  test("on-call schedule detail warns next to email-only roster members", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubCoverage(page);

    const scheduleUid = "sched-coverage";

    await page.route(
      `**/api/v1/orgs/test/on-call-schedules/${scheduleUid}`,
      async (route) => {
        if (route.request().method() !== "GET") {
          await route.continue();

          return;
        }

        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            uid: scheduleUid,
            name: "Primary rotation",
            timezone: "UTC",
            rotationType: "weekly",
            // Zoe is fully covered, Bob is email-only.
            userUids: ["user-zoe", "user-bob"],
            startsAt: new Date().toISOString(),
          }),
        });
      },
    );

    await page.goto(`orgs/test/on-call/${scheduleUid}`);
    await page.waitForLoadState("networkidle");

    // The detail roster renders the member's NAME (name || email), unlike the
    // schedule form's checkbox labels which render the email.
    const bobRow = page.locator("li", { hasText: "Bob" });
    await expect(bobRow).toBeVisible();
    await expect(bobRow.getByTestId("coverage-email-only")).toBeVisible();

    const zoeRow = page.locator("li", { hasText: "Zoe" });
    await expect(zoeRow).toBeVisible();
    await expect(zoeRow.getByTestId("coverage-email-only")).toHaveCount(0);
  });
});
