import { test, expect, type Page } from "./fixtures";

/**
 * E2E for the superadmin Server → Notifications tab (spec 2026-09-03-01).
 *
 * The API is stubbed with `page.route()` and the stub REMEMBERS what the PUT
 * wrote, so the reload assertion is a real round trip rather than a re-render
 * of local state — the bug this guards against is a checkbox that looks saved
 * and silently is not. Delivery itself is covered by the Go tests; what has to
 * hold here is that an operator can see and change who gets paged, and can see
 * when a recipient would be paged into the void.
 */

const EVENTS = ["support.message", "user.registered"];

const ALICE = {
  userUid: "user-alice",
  email: "alice@acme.com",
  name: "Alice",
  superAdmin: true,
  events: [] as string[],
  routes: ["email", "telegram"],
};

// Bob is the whole reason the routes column exists: subscribing him would look
// like it worked and deliver nothing.
const BOB = {
  userUid: "user-bob",
  email: "bob@acme.com",
  name: "Bob",
  superAdmin: true,
  events: [] as string[],
  routes: [] as string[],
};

type TestOutcome = {
  delivered: number;
  failed: number;
  skipped: number;
  routes: number;
};

async function stubOperatorNotifications(
  page: Page,
  testOutcome: TestOutcome = { delivered: 2, failed: 0, skipped: 0, routes: 2 },
): Promise<{ put: () => unknown }> {
  let putBody: unknown = null;

  // Server-side state the stub mutates, so a reload re-reads what was written.
  let stored = {
    enabled: false,
    events: EVENTS,
    recipients: [
      { ...ALICE, events: [] as string[] },
      { ...BOB, events: [] as string[] },
    ],
  };

  await page.route(
    "**/api/v1/system/operator-notifications",
    async (route) => {
      if (route.request().method() === "PUT") {
        putBody = route.request().postDataJSON();
        const sent = putBody as {
          enabled: boolean;
          recipients: { userUid: string; events: string[] }[];
        };

        stored = {
          ...stored,
          enabled: sent.enabled,
          recipients: stored.recipients.map((recipient) => ({
            ...recipient,
            events:
              sent.recipients.find((r) => r.userUid === recipient.userUid)
                ?.events ?? [],
          })),
        };

        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(stored),
        });

        return;
      }

      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(stored),
      });
    },
  );

  await page.route(
    "**/api/v1/system/operator-notifications/test",
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(testOutcome),
      });
    },
  );

  return { put: () => putBody };
}

test.describe("Superadmin operator notifications", () => {
  test("subscribing a recipient survives a reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const captured = await stubOperatorNotifications(page);

    await page.goto("orgs/test/server/notifications");
    await page.waitForLoadState("networkidle");

    const enabled = page.getByTestId("operator-notifications-enabled");
    await expect(enabled).toBeVisible();

    const aliceSupport = page.getByTestId(
      "operator-event-alice@acme.com-support.message",
    );
    await expect(aliceSupport).not.toBeChecked();

    await enabled.click();
    await aliceSupport.click();
    await page.getByRole("button", { name: /save/i }).click();

    await expect(
      page.getByTestId("operator-notifications-saved"),
    ).toBeVisible();

    // What actually went on the wire: the master switch and exactly one
    // subscription.
    const sent = captured.put() as {
      enabled: boolean;
      recipients: { userUid: string; events: string[] }[];
    };
    expect(sent.enabled).toBe(true);
    expect(
      sent.recipients.find((r) => r.userUid === "user-alice")?.events,
    ).toEqual(["support.message"]);

    // And the round trip: reload re-reads the stub's stored document.
    await page.reload();
    await page.waitForLoadState("networkidle");

    await expect(
      page.getByTestId("operator-notifications-enabled"),
    ).toBeChecked();
    await expect(
      page.getByTestId("operator-event-alice@acme.com-support.message"),
    ).toBeChecked();

    // Positive control: the box nobody ticked is still clear, so the assertion
    // above is not just "everything renders checked".
    await expect(
      page.getByTestId("operator-event-alice@acme.com-user.registered"),
    ).not.toBeChecked();
  });

  test("a recipient with no notification route is flagged", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubOperatorNotifications(page);

    await page.goto("orgs/test/server/notifications");
    await page.waitForLoadState("networkidle");

    const bobRow = page.getByTestId("operator-recipient-bob@acme.com");
    await expect(bobRow).toContainText(/no notification routes/i);

    // Positive control: the recipient who HAS routes shows them instead of the
    // warning, so the amber text is not on every row.
    const aliceRow = page.getByTestId("operator-recipient-alice@acme.com");
    await expect(aliceRow).toContainText("telegram");
    await expect(aliceRow).not.toContainText(/no notification routes/i);
  });

  test("the test button reports a successful delivery", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubOperatorNotifications(page);

    await page.goto("orgs/test/server/notifications");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("operator-notifications-test").click();

    await expect(
      page.getByTestId("operator-notifications-test-result"),
    ).toContainText(/delivered/i);
  });

  test("the test button reports an undeliverable setup instead of a bare success", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubOperatorNotifications(page, {
      delivered: 0,
      failed: 0,
      skipped: 0,
      routes: 0,
    });

    await page.goto("orgs/test/server/notifications");
    await page.waitForLoadState("networkidle");

    await page.getByTestId("operator-notifications-test").click();

    // A 200 with nothing delivered must NOT read as success: that is the exact
    // state an operator presses this button to discover.
    await expect(
      page.getByTestId("operator-notifications-test-result"),
    ).toContainText(/nothing was delivered/i);
  });
});
