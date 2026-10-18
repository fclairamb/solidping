import { test, expect, DASH_BASE } from "./fixtures";

/**
 * The incident detail header must wrap instead of overflowing: an active,
 * escalated, flapping incident has a long title, four badges and five
 * actions, which used to push Resolve past the right edge at 1280x800.
 *
 * The detail endpoint is mocked so the incident is deterministic.
 */

const INCIDENT_UID = "33333333-3333-3333-3333-333333333399";

const INCIDENT = {
  uid: INCIDENT_UID,
  number: 13,
  checkUid: "22222222-2222-2222-2222-222222222299",
  checkSlug: "checkout",
  checkName: "checkout",
  state: "active",
  title: "checkout is down",
  startedAt: "2026-10-14T10:00:00Z",
  escalatedAt: "2026-10-14T10:30:00Z",
  failureCount: 5,
  flapLevel: 1,
};

for (const viewport of [
  { width: 1280, height: 800 },
  { width: 375, height: 812 },
]) {
  test(`incident detail header fits at ${viewport.width}x${viewport.height}`, async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize(viewport);

    await page.route(
      new RegExp(`/api/v1/orgs/[^/]+/incidents/${INCIDENT_UID}(\\?.*)?$`),
      (route) =>
        route.request().method() === "GET"
          ? route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify(INCIDENT),
            })
          : route.fallback(),
    );

    await page.goto(`${DASH_BASE}/orgs/test/incidents/${INCIDENT_UID}`);
    await expect(page.getByTestId("incident-detail-header")).toBeVisible();
    await expect(
      page.getByTestId("incident-detail-header").getByText("Escalated", { exact: true }),
    ).toBeVisible();

    if (viewport.width >= 1024) {
      await expect(page.getByTestId("app-sidebar")).toBeVisible();
    }

    const resolve = page.getByTestId("incident-resolve-button");
    await expect(resolve).toBeVisible();
    const box = await resolve.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.x + box!.width).toBeLessThanOrEqual(viewport.width);

    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);

    await resolve.click({ trial: true });
    for (const id of ["refresh", "snooze"]) {
      await expect(page.getByTestId(`incident-${id}-button`)).toBeVisible();
    }
  });
}
