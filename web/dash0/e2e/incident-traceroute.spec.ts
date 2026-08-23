import { test, expect } from "./fixtures";

// Coverage for spec 2026-08-21-10: the incident path-trace card.
//
// Deterministically seeded in test mode (server/test/testdata/testdata.go,
// createTestIncidentTraceroute): a down TCP check whose active incident carries
// a REAL traceroute attachment — a nettrace.Capture written through the normal
// attachment path, so the signed download URL serves JSON the card actually
// fetches and parses.
//
// Incident 26 (createTestIncidentScreenshot) is the negative: it has a
// screenshot attachment but no path capture, so its page must render the
// screenshot card and NOT this one.
const TRACE_INCIDENT = "00000000-0000-0000-0000-000000000028";
const NO_TRACE_INCIDENT = "00000000-0000-0000-0000-000000000026";

test.describe("Incident traceroute attachment", () => {
  test("renders the hop table with loss, RTT, mode and region", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/incidents/${TRACE_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("incident-traceroute-card");
    await expect(card).toBeVisible();

    // The capture is fetched from its own signed URL, so the table appearing at
    // all proves the round trip worked end to end.
    const table = page.getByTestId("incident-traceroute-table");
    await expect(table).toBeVisible();

    // Hop 1: a named router with a real RTT.
    const first = page.getByTestId("incident-traceroute-hop-1");
    await expect(first).toContainText("gw.acme.com");
    await expect(first).toContainText("10.0.0.1");
    await expect(first).toContainText("1.5");

    // Hop 2 answered nothing. The counts sit next to the percentage so 100% of
    // one probe cannot be mistaken for 100% of three.
    const silent = page.getByTestId("incident-traceroute-hop-2");
    await expect(silent).toContainText("100");
    await expect(silent).toContainText("0/3");
    await expect(silent).toContainText("no reply");

    // Hop 3 is the target itself.
    await expect(page.getByTestId("incident-traceroute-hop-3")).toContainText(
      "192.0.2.10",
    );

    // Mode and probing region are labelled — the spec's surfacing requirement,
    // and what stops a hop list from being read out of context.
    await expect(page.getByTestId("incident-traceroute-mode")).toContainText(
      "ICMP",
    );
    await expect(page.getByTestId("incident-traceroute-region")).toContainText(
      "eu-west",
    );

    // The capture is honestly captioned as taken AFTER the failing probe.
    await expect(page.getByTestId("incident-traceroute-caption")).toContainText(
      "after the failing probe was already reported",
    );

    // An ICMP-mode capture can see hop addresses, so the blind-mode notice must
    // NOT be shown — it is reserved for the TCP fallback.
    await expect(
      page.getByTestId("incident-traceroute-blind-notice"),
    ).toHaveCount(0);
  });

  test("the card is absent entirely when the incident has no path capture", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto(`/dash0/orgs/test/incidents/${NO_TRACE_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    // Positive control: this incident's page really did render — it has the
    // screenshot card — so the absence below is about the attachment kind and
    // not about a page that failed to load.
    await expect(page.getByTestId("incident-screenshot-card")).toBeVisible();
    await expect(page.getByTestId("incident-traceroute-card")).toHaveCount(0);
  });

  test("the hop table scrolls inside itself on a phone", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.setViewportSize({ width: 375, height: 812 });
    await page.goto(`/dash0/orgs/test/incidents/${TRACE_INCIDENT}`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("incident-traceroute-table")).toBeVisible();

    // The page itself must never scroll sideways: wide content lives in its own
    // overflow container (frontend convention, design reference).
    const overflows = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflows).toBe(false);
  });
});
