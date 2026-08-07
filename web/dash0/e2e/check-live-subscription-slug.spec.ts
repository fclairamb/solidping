import type { WebSocket } from "@playwright/test";
import { test, expect, type Page, API_BASE } from "./fixtures";

// Regression coverage for the bug where the check detail page subscribed to
// the realtime WS scope with the raw `$checkUid` route param, which may be a
// slug — the server's handleSubscribe validates check scopes with a
// uid-only lookup, so a slug-keyed subscribe was rejected with a NOT_FOUND
// `error` frame, silently swallowed client-side (the page just kept
// polling, with nothing telling the user or a developer live updates were
// broken). The fix: the page now always subscribes with `check.uid` from
// the already-loaded check response, and any WS subscription error is
// surfaced via a visible, non-blocking badge instead of being dropped.

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
      config: { url: "https://example.com/check-live-subscription-slug" },
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

/** Collects every `subscribe` frame sent on the realtime v2 socket for a
 * `check` scope, keyed by the uid/slug it carries — lets a test assert the
 * client sent the canonical uid, never the raw route param. */
function watchCheckSubscribeFrames(page: Page): { frames: string[] } {
  const state = { frames: [] as string[] };
  page.on("websocket", (ws: WebSocket) => {
    if (!ws.url().includes("/events/ws")) return;
    ws.on("framesent", (frame) => {
      const text = typeof frame.payload === "string" ? frame.payload : "";
      if (text.includes('"type":"subscribe"') && text.includes('"entity":"check"')) {
        state.frames.push(text);
      }
    });
  });
  return state;
}

test.describe("Check live subscription: slug vs uid", () => {
  test("navigating via the check's slug subscribes with the canonical uid and goes live, no error badge", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const slug = `e2e-live-slug-${Date.now()}`;
    const check = await createCheckWithSlug(page, token, `E2E Live Slug ${Date.now()}`, slug);

    try {
      const subscribeFrames = watchCheckSubscribeFrames(page);

      await page.goto(`orgs/test/checks/${check.slug}`);
      await expect(page.getByTestId("check-detail-header")).toBeVisible();

      // The subscribe frame for this check's scope must carry the uid, never
      // the slug — this is the exact frame shape the bug report showed going
      // to the server and getting rejected.
      await expect
        .poll(() => subscribeFrames.frames, { timeout: 10000 })
        .toEqual(
          expect.arrayContaining([
            JSON.stringify({ type: "subscribe", entity: "check", uid: check.uid }),
          ]),
        );
      expect(
        subscribeFrames.frames.some((f) => f.includes(`"uid":"${slug}"`)),
        "must never subscribe with the raw slug",
      ).toBe(false);

      // No degraded-mode indicator — the subscription succeeds.
      await expect(page.getByTestId("check-live-error-badge")).toHaveCount(0);
    } finally {
      await deleteCheck(page, token, check.uid);
    }
  });

  test("navigating to a nonexistent check still uses the existing page-level not-found path", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`orgs/test/checks/does-not-exist-${Date.now()}`);
    await page.waitForLoadState("networkidle");

    // Unaffected by this change: QueryErrorView's friendly not-found card,
    // not a crash and not the (unrelated) live-error badge.
    await expect(page.getByText(/not found/i).first()).toBeVisible();
    await expect(page.getByTestId("check-live-error-badge")).toHaveCount(0);
  });
});
