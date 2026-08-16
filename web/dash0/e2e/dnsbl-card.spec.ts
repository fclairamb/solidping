import { test, expect } from "./fixtures";

/**
 * DnsblCard rendering (spec 2026-07-10-14): the DNSBL result card must turn the
 * reserved 127.255.255.x Spamhaus status octets into human-readable labels, and
 * keep showing a genuine 127.0.0.x listing verbatim on a destructive badge.
 *
 * Results aren't creatable via the public API, so — like
 * check-result-detail-navigation.spec.ts — we mock the single-result endpoint
 * and deep-link straight to the result-detail page, which mounts DnsblCard
 * unconditionally (it self-detects DNSBL output and returns null otherwise).
 */

const checkUid = "bbbbbbbb-0000-7000-8000-0000000000cc";
const resultUid = "bbbbbbbb-0000-7000-8000-0000000000dd";
const zone = "zen.spamhaus.org";

function mockResult(output: Record<string, unknown>) {
  return {
    uid: resultUid,
    status: "down",
    durationMs: 42,
    periodStart: "2026-07-10T00:00:00Z",
    periodType: "raw",
    output,
  };
}

test.describe("DNSBL result card", () => {
  test("renders reserved status octets as human-readable labels, not raw IPs", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // A .254 = "public/open resolver refused" reply — the exact false-positive
    // scenario (api.acme.io on an AWS resolver) this card exists to explain.
    await page.route("**/api/v1/orgs/*/checks/*/results/*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          mockResult({
            listed_on: [],
            clean: [],
            inconclusive: [zone],
            error_codes: { [zone]: ["127.255.255.254"] },
          }),
        ),
      }),
    );

    await page.goto(`orgs/test/checks/${checkUid}/results/${resultUid}`);
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("dnsbl-card");
    await expect(card).toBeVisible();

    // The primary ask: the plain-language label renders...
    await expect(
      card.getByText("Query via public/open resolver — refused"),
    ).toBeVisible();
    // ...and the raw reserved octet is nowhere on the page (neither in the card
    // nor in a raw-JSON dump — those keys are stripped from the dump).
    await expect(page.getByText("127.255.255.254")).toHaveCount(0);

    // The inconclusive zone shows as a secondary badge.
    await expect(card.getByTestId(`dnsbl-inconclusive-${zone}`)).toContainText(
      zone,
    );
  });

  test("a genuine 127.0.0.x listing stays a destructive badge with the raw code", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // No-regression case: a real SBL listing (127.0.0.2) must NOT be relabelled —
    // operators want the sub-list code verbatim on a red badge.
    await page.route("**/api/v1/orgs/*/checks/*/results/*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          mockResult({
            listed_on: [zone],
            clean: [],
            inconclusive: [],
            return_codes: { [zone]: ["127.0.0.2"] },
          }),
        ),
      }),
    );

    await page.goto(`orgs/test/checks/${checkUid}/results/${resultUid}`);
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("dnsbl-card");
    await expect(card).toBeVisible();

    const listedBadge = card.getByTestId(`dnsbl-listed-${zone}`);
    await expect(listedBadge).toBeVisible();
    await expect(listedBadge).toContainText(zone);
    await expect(listedBadge).toContainText("127.0.0.2");
  });
});
