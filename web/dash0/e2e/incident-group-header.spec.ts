import { test, expect } from "./fixtures";

/**
 * Read-time aggregation of per-check incidents (spec 2026-08-24-14).
 *
 * Incidents are per-check now, so a group whose members go down produces one
 * incident each. The consolidated "RabbitMQ — 2/6 down" view is rebuilt at
 * DISPLAY time: a plain, non-interactive header row with the member incidents
 * beneath it, grouped client-side with no API change.
 *
 * Fully deterministic — every list endpoint the page reads is mocked, so this
 * asserts the rendering rule rather than whatever the seeded fixture happens
 * to contain.
 */

const GROUP_UID = "11111111-1111-1111-1111-111111111111";

const CHECK_PROD = "22222222-2222-2222-2222-222222222221";
const CHECK_NONPROD = "22222222-2222-2222-2222-222222222222";
const CHECK_SOLO = "22222222-2222-2222-2222-222222222223";

const INCIDENT_PROD = "33333333-3333-3333-3333-333333333331";
const INCIDENT_NONPROD = "33333333-3333-3333-3333-333333333332";
const INCIDENT_SOLO = "33333333-3333-3333-3333-333333333333";

const CHECKS = [
  { uid: CHECK_PROD, name: "rabbitmq-prod", slug: "rabbitmq-prod", type: "tcp", checkGroupUid: GROUP_UID },
  { uid: CHECK_NONPROD, name: "rabbitmq-nonprod", slug: "rabbitmq-nonprod", type: "tcp", checkGroupUid: GROUP_UID },
  { uid: CHECK_SOLO, name: "payments-api", slug: "payments-api", type: "http" },
];

const GROUPS = [
  {
    uid: GROUP_UID,
    name: "RabbitMQ",
    slug: "rabbitmq",
    sortOrder: 1,
    checkCount: 6,
    status: "degraded",
    createdAt: "2026-08-01T00:00:00Z",
    updatedAt: "2026-08-01T00:00:00Z",
  },
];

function incident(uid: string, checkUid: string, checkSlug: string, title: string) {
  return {
    uid,
    number: 1,
    checkUid,
    checkSlug,
    checkName: checkSlug,
    state: "active",
    title,
    startedAt: "2026-08-23T23:23:30Z",
    failureCount: 3,
  };
}

// Deliberately interleaved: the solo incident sits BETWEEN the two group
// members, so a grouping pass that reordered the list rather than pulling the
// second member up to its group would be visible here.
const INCIDENTS = [
  incident(INCIDENT_NONPROD, CHECK_NONPROD, "rabbitmq-nonprod", "rabbitmq-nonprod is down"),
  incident(INCIDENT_SOLO, CHECK_SOLO, "payments-api", "payments-api is down"),
  incident(INCIDENT_PROD, CHECK_PROD, "rabbitmq-prod", "rabbitmq-prod is down"),
];

test.describe("Incidents list: check-group header", () => {
  test("groups members under a plain 'RabbitMQ — 2/6 down' header, ungrouped rows stay bare", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    const json = (body: unknown) => ({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });

    await page.route("**/api/v1/orgs/*/incidents**", (route) =>
      route.fulfill(
        json({
          data: INCIDENTS,
          pagination: { total: INCIDENTS.length, size: INCIDENTS.length },
        }),
      ),
    );
    await page.route("**/api/v1/orgs/*/checks**", (route) =>
      route.fulfill(json({ data: CHECKS, pagination: { total: CHECKS.length } })),
    );
    await page.route("**/api/v1/orgs/*/check-groups**", (route) =>
      route.fulfill(json({ data: GROUPS })),
    );

    await page.goto("/dash0/orgs/test/incidents");
    await page.waitForLoadState("networkidle");

    // Exactly one header, for the one group that has members in hand.
    const header = page.getByTestId("incident-group-header");
    await expect(header).toHaveCount(1);
    await expect(header).toHaveAttribute("data-check-group-uid", GROUP_UID);

    // N = the member incidents loaded and active; M = the group's enabled
    // checks. The literal phrasing is what the outage report asked for.
    await expect(header).toContainText("RabbitMQ");
    await expect(header).toContainText("2/6");

    // Plain header: no expand/collapse affordance to click.
    await expect(header.getByRole("button")).toHaveCount(0);

    // Both members render as their OWN incident rows beneath it — the whole
    // point of the change, since the second member used to have no row at all.
    const rows = page.getByTestId("incident-row");
    await expect(rows).toHaveCount(3);

    const uids = await rows.evaluateAll((els) =>
      els.map((el) => el.getAttribute("data-incident-uid")),
    );
    // The group takes the position of its FIRST member; the later member joins
    // it; the ungrouped incident keeps its own place after them.
    expect(uids).toEqual([INCIDENT_NONPROD, INCIDENT_PROD, INCIDENT_SOLO]);

    // The ungrouped incident is not under any header.
    const soloRow = page.locator(`[data-incident-uid="${INCIDENT_SOLO}"]`);
    await expect(soloRow).toBeVisible();
  });
});
