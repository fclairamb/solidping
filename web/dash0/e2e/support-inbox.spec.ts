import { test, expect, type Page } from "./fixtures";

/**
 * E2E for the unlinked instance support inbox (spec 2026-08-22-02).
 *
 * The API is stubbed with `page.route()` rather than seeded through a real
 * WhatsApp webhook: the interesting behaviour here is the UI's, and the capture
 * path itself is covered by the Go handler tests. What has to hold is the split
 * between the two independent axes (status vs reply window), that an expired
 * WhatsApp thread disables the reply box WITH THE REASON, and that a
 * non-superadmin gets Permission Denied instead of a redirect loop.
 */

const NOW = Date.now();

function iso(offsetMs: number): string {
  return new Date(NOW + offsetMs).toISOString();
}

const ACTIVE_THREAD = {
  uid: "thread-active",
  channel: "telegram",
  channelIdentity: "123456789",
  subject: "@alice: is the api down for you too?",
  status: "open",
  lastMessageAt: iso(-60_000),
  lastInboundAt: iso(-60_000),
  unreadCount: 2,
  createdAt: iso(-120_000),
  updatedAt: iso(-60_000),
  replyWindow: { expires: false, open: true, costsMoney: false },
  canReply: true,
};

// Open, and yet unanswerable — the state the spec calls out as the one an
// operator most needs to see.
const EXPIRED_THREAD = {
  uid: "thread-expired",
  channel: "whatsapp",
  channelIdentity: "+33600000000",
  subject: "+33600000000: the page never loaded",
  status: "open",
  lastMessageAt: iso(-30 * 3600_000),
  lastInboundAt: iso(-30 * 3600_000),
  unreadCount: 1,
  createdAt: iso(-30 * 3600_000),
  updatedAt: iso(-30 * 3600_000),
  replyWindow: {
    expires: true,
    open: false,
    expiresAt: iso(-6 * 3600_000),
    reason:
      "the 24-hour WhatsApp customer-service window has lapsed — only an approved template may be sent now",
    costsMoney: false,
  },
  canReply: true,
};

const CLOSED_THREAD = {
  uid: "thread-closed",
  channel: "sms",
  channelIdentity: "+33611111111",
  subject: "+33611111111: false alarm, sorry",
  status: "closed",
  lastMessageAt: iso(-5 * 86_400_000),
  lastInboundAt: iso(-5 * 86_400_000),
  unreadCount: 0,
  createdAt: iso(-5 * 86_400_000),
  updatedAt: iso(-5 * 86_400_000),
  replyWindow: { expires: false, open: true, costsMoney: true },
  canReply: true,
};

const MESSAGES = [
  {
    uid: "msg-1",
    threadUid: ACTIVE_THREAD.uid,
    channel: "telegram",
    direction: "inbound",
    body: "is the api down for you too?",
    rawType: "text",
    createdAt: iso(-60_000),
  },
  {
    uid: "msg-2",
    threadUid: ACTIVE_THREAD.uid,
    channel: "telegram",
    direction: "outbound",
    body: "looking into it now",
    rawType: "text",
    createdAt: iso(-30_000),
  },
];

async function stubSupportApi(page: Page, threads: unknown[]) {
  await page.route("**/api/v1/support/threads?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: threads }),
    });
  });

  await page.route("**/api/v1/support/threads", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: threads }),
    });
  });

  await page.route("**/api/v1/support/threads/*/messages*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: MESSAGES }),
    });
  });

  await page.route("**/api/v1/support/threads/thread-active", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(ACTIVE_THREAD),
    });
  });

  await page.route("**/api/v1/support/threads/thread-expired", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(EXPIRED_THREAD),
    });
  });
}

test.describe("Support inbox", () => {
  test("splits threads by reply window, not just by status", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubSupportApi(page, [ACTIVE_THREAD, EXPIRED_THREAD, CLOSED_THREAD]);

    await page.goto("support");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("support-thread-list")).toBeVisible();

    // Both of these are status=open; only the reply window separates them.
    const active = page.getByTestId("support-section-active");
    const expired = page.getByTestId("support-section-expired");
    const closed = page.getByTestId("support-section-closed");

    await expect(active).toBeVisible();
    await expect(expired).toBeVisible();
    await expect(closed).toBeVisible();

    await expect(active.getByTestId("support-thread-row")).toHaveCount(1);
    await expect(expired.getByTestId("support-thread-row")).toHaveCount(1);
    await expect(closed.getByTestId("support-thread-row")).toHaveCount(1);

    await expect(active).toContainText("is the api down for you too?");
    await expect(expired).toContainText("the page never loaded");
    await expect(active.getByTestId("support-unread")).toBeVisible();
  });

  test("thread detail reads as a chat and can be replied to", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubSupportApi(page, [ACTIVE_THREAD, EXPIRED_THREAD, CLOSED_THREAD]);

    let sentBody = "";
    await page.route(
      "**/api/v1/support/threads/thread-active/messages",
      async (route) => {
        if (route.request().method() !== "POST") {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            body: JSON.stringify({ data: MESSAGES }),
          });

          return;
        }

        sentBody = JSON.parse(route.request().postData() || "{}").body;
        await route.fulfill({
          status: 201,
          contentType: "application/json",
          body: JSON.stringify({
            uid: "msg-3",
            threadUid: ACTIVE_THREAD.uid,
            channel: "telegram",
            direction: "outbound",
            body: sentBody,
            rawType: "text",
            createdAt: new Date().toISOString(),
          }),
        });
      },
    );

    await page.goto("support/thread-active");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("support-message-inbound")).toContainText(
      "is the api down for you too?",
    );
    await expect(page.getByTestId("support-message-outbound")).toContainText(
      "looking into it now",
    );

    const box = page.getByTestId("support-reply-box");
    await expect(box).toBeVisible();

    await page.getByTestId("support-reply-input").fill("we found it, deploying a fix");
    await page.getByTestId("support-reply-send").click();

    await expect
      .poll(() => sentBody, { timeout: 10_000 })
      .toBe("we found it, deploying a fix");
  });

  test("an expired WhatsApp thread disables the reply box and says why", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubSupportApi(page, [ACTIVE_THREAD, EXPIRED_THREAD, CLOSED_THREAD]);

    await page.goto("support/thread-expired");
    await page.waitForLoadState("networkidle");

    // The reason is SHOWN, not discovered from a provider error at send time.
    const blocked = page.getByTestId("support-reply-blocked");
    await expect(blocked).toBeVisible();
    await expect(blocked).toContainText("24-hour");

    await expect(page.getByTestId("support-reply-box")).toHaveCount(0);
    await expect(page.getByTestId("support-reply-input")).toHaveCount(0);

    // Positive control: the answerable thread DOES get a box, so the assertion
    // above is not passing because the box never renders.
    await page.goto("support/thread-active");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("support-reply-box")).toBeVisible();
  });

  test("a non-superadmin gets Permission Denied and is never redirected", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Downgrade the session's role. /support is unlinked, but a hidden route is
    // not an access control — the URL is public knowledge.
    await page.route("**/api/v1/auth/me", async (route) => {
      const response = await route.fetch();
      const body = await response.json();

      if (body?.user) {
        body.user.role = "admin";
      }

      await route.fulfill({
        status: response.status(),
        contentType: "application/json",
        body: JSON.stringify(body),
      });
    });

    await page.goto("support");
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("support-permission-denied")).toBeVisible();
    // Never a redirect: an authenticated-but-unauthorized user must not loop.
    expect(page.url()).toContain("/support");
    expect(page.url()).not.toContain("/login");
  });
});
