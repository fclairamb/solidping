import { test, expect, type Page } from "./fixtures";

// Spec 2026-09-28-02: `agent.connected` / `agent.disconnected` rows used to
// read "Agent Connected | System | (nothing)" for every agent — a page full
// of them never said WHICH agent, WHICH private location, or WHY it dropped,
// even though the payload already carries target_name / region / reason.
// These tests pin the three answers the row now gives, and the Related chip
// that opens the Private Locations page.

async function mockEvents(page: Page, events: unknown[]) {
  await page.route("**/api/v1/orgs/*/events*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ data: events, pagination: { total: events.length } }),
    }),
  );
}

const AGENT_NAME = "74782dfcd3e5";
const AGENT_REGION = "@acme-paris";

function agentEvent(
  eventType: "agent.connected" | "agent.disconnected",
  payload: Record<string, unknown>,
  uid: string,
) {
  return {
    uid,
    eventType,
    actorType: "system",
    payload: {
      target_type: "agent",
      target_uid: `ag-${uid}`,
      ...payload,
    },
    createdAt: new Date().toISOString(),
  };
}

test.describe("Events page names the agent", () => {
  test("an agent.disconnected row shows name, region and reason, and links to Private locations", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockEvents(page, [
      agentEvent(
        "agent.disconnected",
        {
          target_name: AGENT_NAME,
          region: AGENT_REGION,
          reason: "ping_timeout",
        },
        "evt-agent-disconnected",
      ),
    ]);

    await page.goto("orgs/test/events");
    await page.waitForLoadState("networkidle");

    const row = page.getByRole("row", { name: /Agent Disconnected/ });
    await expect(
      row.getByText(`${AGENT_NAME} · ${AGENT_REGION} · ping timeout`, {
        exact: true,
      }),
    ).toBeVisible();

    const chip = row.getByRole("link", { name: AGENT_NAME });
    await expect(chip).toBeVisible();
    await expect(chip).toHaveAttribute("href", /private-locations/);
  });

  test("an agent.connected row shows name and region without a reason", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockEvents(page, [
      agentEvent(
        "agent.connected",
        { target_name: AGENT_NAME, region: AGENT_REGION },
        "evt-agent-connected",
      ),
    ]);

    await page.goto("orgs/test/events");
    await page.waitForLoadState("networkidle");

    const row = page.getByRole("row", { name: /Agent Connected/ });
    await expect(
      row.getByText(`${AGENT_NAME} · ${AGENT_REGION}`, { exact: true }),
    ).toBeVisible();
    // The reason only exists on a disconnect — a connect row must not
    // inherit one from anywhere.
    await expect(row.getByText(/ping timeout|revoked|server shutdown/)).toHaveCount(0);

    await expect(row.getByRole("link", { name: AGENT_NAME })).toHaveAttribute(
      "href",
      /private-locations/,
    );
  });

  test("a region-only disconnect falls back to the region in the Related chip", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockEvents(page, [
      agentEvent(
        "agent.disconnected",
        { region: AGENT_REGION, reason: "revoked" },
        "evt-agent-region-only",
      ),
    ]);

    await page.goto("orgs/test/events");
    await page.waitForLoadState("networkidle");

    const row = page.getByRole("row", { name: /Agent Disconnected/ });
    await expect(
      row.getByText(`${AGENT_REGION} · revoked`, { exact: true }),
    ).toBeVisible();

    await expect(row.getByRole("link", { name: AGENT_REGION })).toHaveAttribute(
      "href",
      /private-locations/,
    );
  });

  test("an agent row with no payload identity renders no second line and no chip", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await mockEvents(page, [
      agentEvent("agent.disconnected", {}, "evt-agent-anonymous"),
    ]);

    await page.goto("orgs/test/events");
    await page.waitForLoadState("networkidle");

    const row = page.getByRole("row", { name: /Agent Disconnected/ });
    await expect(row.locator("div.pl-6")).toHaveCount(0);
    await expect(
      row.getByRole("link", { name: new RegExp(AGENT_NAME) }),
    ).toHaveCount(0);
  });
});
