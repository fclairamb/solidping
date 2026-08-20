import { test, expect } from "./fixtures";

// Coverage for spec 2026-08-20-01: the "What the probe saw" card on the
// incident detail page, which renders the opt-in capture of the response a
// failing HTTP check received.
//
// Deterministically seeded in test mode (server/test/testdata/testdata.go,
// createTestFailureResponseCaptures): three incidents carrying a
// `details.failureResponse` block, one per visual state the card has.
// Incident 13 (createTestIncidentNotification) carries NO capture and is the
// negative case — modeled on incident-failure-details.spec.ts, which pins the
// sibling card on that same incident.
const TEXT_INCIDENT = "00000000-0000-0000-0000-000000000017";
const TRUNCATED_INCIDENT = "00000000-0000-0000-0000-000000000019";
const BINARY_INCIDENT = "00000000-0000-0000-0000-000000000021";
const NO_CAPTURE_INCIDENT = "00000000-0000-0000-0000-000000000013";

test.describe("Incident probe-response capture", () => {
  test("renders the status line, metadata, headers table and collapsed body", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/incidents/${TEXT_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("probe-response-card");
    await expect(card).toBeVisible();
    await expect(card).toContainText("What the probe saw");

    // Status line and the probed URL.
    await expect(page.getByTestId("probe-response-status-line")).toHaveText(
      "HTTP/1.1 503 Service Unavailable",
    );
    await expect(card).toContainText("https://acme.com/health");

    // Capture timestamp and probing region sit next to the content — the card
    // must never read as "the target's current state".
    const meta = page.getByTestId("probe-response-meta");
    await expect(meta).toContainText("Captured at");
    await expect(meta).toContainText("Probed from");
    await expect(meta).toContainText("eu");
    await expect(meta).toContainText("203.0.113.10:443");

    // Headers table, with the already-redacted value rendered as such.
    const headers = page.getByTestId("probe-response-headers-table");
    await expect(headers).toBeVisible();
    await expect(headers).toContainText("Set-Cookie");
    await expect(headers).toContainText("[redacted]");
    await expect(headers).toContainText("X-Request-Id");
    await expect(headers).toContainText("req-4711");

    // The redaction note explains the guarantee, including that request
    // headers are never captured.
    await expect(card).toContainText("Response headers only");
    await expect(card).toContainText("request headers are never captured");

    // Body viewer is collapsed by default and expands to the captured body.
    const body = card.locator("details", { hasText: "Response body" });
    await expect(body).toBeVisible();
    await expect(body).not.toHaveAttribute("open", "");

    await body.locator("summary").click();
    await expect(body.locator("pre")).toContainText("acme origin unreachable");

    // Neither special-case notice belongs on an ordinary text capture.
    await expect(
      page.getByTestId("probe-response-truncated-notice"),
    ).toHaveCount(0);
    await expect(page.getByTestId("probe-response-binary-notice")).toHaveCount(
      0,
    );
  });

  test("shows an explicit notice when the body was truncated", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/incidents/${TRUNCATED_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("probe-response-card");
    await expect(card).toBeVisible();

    const notice = page.getByTestId("probe-response-truncated-notice");
    await expect(notice).toBeVisible();
    // The cap AND the original Content-Length, so the reader knows how much of
    // the page they are not seeing.
    await expect(notice).toContainText("16.0 KB");
    await expect(notice).toContainText("48.0 KB");

    // A truncated body is still a body: it renders, with its marker.
    const body = card.locator("details", { hasText: "Response body" });
    await expect(body).toBeVisible();
    await body.locator("summary").click();
    await expect(body.locator("pre")).toContainText("[truncated]");

    await expect(page.getByTestId("probe-response-binary-notice")).toHaveCount(
      0,
    );
  });

  test("shows metadata only, and no body, for a binary response", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/incidents/${BINARY_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("probe-response-card");
    await expect(card).toBeVisible();

    const notice = page.getByTestId("probe-response-binary-notice");
    await expect(notice).toBeVisible();
    await expect(notice).toContainText("Body not stored");
    await expect(notice).toContainText("2.0 KB");
    await expect(notice).toContainText("SHA-256");

    // Positive control that the card is genuinely populated, followed by the
    // negative that matters: no body viewer at all.
    await expect(page.getByTestId("probe-response-status-line")).toHaveText(
      "HTTP/1.1 502 Bad Gateway",
    );
    await expect(
      card.locator("details", { hasText: "Response body" }),
    ).toHaveCount(0);

    await expect(
      page.getByTestId("probe-response-truncated-notice"),
    ).toHaveCount(0);
  });

  test("the card is absent entirely when the incident carries no capture", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Positive control first: on an incident that HAS a capture the card is
    // rendered, so "absent" below is evidence of the guard and not of a
    // broken page or a wrong test id.
    await page.goto(`/dash0/orgs/test/incidents/${TEXT_INCIDENT}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("probe-response-card")).toBeVisible();

    await page.goto(`/dash0/orgs/test/incidents/${NO_CAPTURE_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    // The page really did render (its sibling failure card is there) — it is
    // only the probe-response card that is withheld.
    await expect(page.getByTestId("failure-details-card")).toBeVisible();
    await expect(page.getByTestId("probe-response-card")).toHaveCount(0);
  });

  test("the card is usable on mobile", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(`/dash0/orgs/test/incidents/${TEXT_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("probe-response-card")).toBeVisible();
    await expect(
      page.getByTestId("probe-response-headers-table"),
    ).toBeVisible();

    const hasOverflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(hasOverflow).toBe(false);
  });
});
