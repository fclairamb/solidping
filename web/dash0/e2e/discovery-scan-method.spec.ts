import { test, expect } from "@playwright/test";

/**
 * LOCAL-ONLY end-to-end coverage for the discovery scan-method form.
 *
 * These are intentionally excluded from CI (`test.skip` on `CI`) and only run on
 * a developer's machine via `bun run test:e2e`:
 *  - the Kubernetes case creates a real cluster *connection* in the shared
 *    test-mode org, which would otherwise race the CI suite's
 *    "kubernetes hidden when no cluster" assertion;
 *  - they exercise live URL/router behavior that we keep out of the deterministic
 *    CI lane.
 *
 * Covers two pieces of feedback:
 *  1. the selected scan method drives the route (a `?method=` search param);
 *  2. "Start scan" must be ENABLE-able for Kubernetes once a cluster connection
 *     exists and is selected (previously it stayed disabled).
 */
// Excluded from CI: real CI sets CI=true and never sets E2E_LOCAL, so these skip
// there. They run on any local `bun run test:e2e` (CI unset), and can be
// force-run against an already-running server with `CI=true E2E_LOCAL=1`.
const SKIP_IN_CI = !!process.env.CI && !process.env.E2E_LOCAL;

// REST base for direct login/cleanup calls; override when the server isn't on
// the default port (e.g. a side-car test server).
const API_BASE = process.env.E2E_API_BASE ?? "http://localhost:4000";

test.describe("Discovery scan-method routing + kubernetes enablement (local-only)", () => {
  test.skip(SKIP_IN_CI, "local-only: excluded from CI");

  test.beforeEach(async ({ page }) => {
    await page.goto("/dash0/orgs/test/login");
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();
    await page.waitForURL((url) => !url.pathname.includes("login"));
  });

  test("selecting a scan method writes it to the URL (?method=)", async ({
    page,
  }) => {
    await page.goto("/dash0/orgs/test/discovery/new");
    // Defaults to LAN — CIDR fields shown, no method param needed.
    await expect(page.getByLabel(/cidr/i)).toBeVisible();

    // Pick "Containers" from the method select.
    await page.getByRole("combobox", { name: /scan method/i }).click();
    await page.getByRole("option", { name: /containers/i }).click();

    // The selection is reflected in the URL, and the container form renders.
    await expect(page).toHaveURL(/method=container/);
    await expect(page.getByLabel(/container host/i)).toBeVisible();
  });

  test("deep-linking ?method=container renders the container form on load", async ({
    page,
  }) => {
    // The form honors the URL param with no clicks (bookmarkable / refresh-safe).
    await page.goto("/dash0/orgs/test/discovery/new?method=container");
    await expect(page.getByLabel(/container host/i)).toBeVisible();
    await expect(
      page.getByRole("combobox", { name: /scan method/i }),
    ).toHaveText(/containers/i);
  });

  test("Kubernetes: Start scan enables once a cluster exists and confirm is checked", async ({
    page,
  }) => {
    // 1. Create a kubernetes cluster connection so the method is offered at all
    //    (it is capability-gated on having ≥1 connection).
    await page.goto("/dash0/orgs/test/integrations/new?type=kubernetes");
    await expect(page.getByTestId("kubernetes-panel")).toBeVisible();
    const clusterName = `E2E Disco K8s ${Date.now()}`;
    await page.getByLabel("Name").fill(clusterName);
    await page
      .getByTestId("kubernetes-api-server")
      .fill("https://10.0.0.1:6443");
    await page.getByTestId("kubernetes-token").fill("super-secret-sa-token");
    await page.getByRole("button", { name: /create integration/i }).click();
    await page.waitForURL((url) =>
      /\/integrations\/[0-9a-f-]{36}$/.test(url.pathname),
    );
    const clusterUid = page.url().split("/").pop() as string;

    try {
      // 2. Deep-link straight to the Kubernetes scan method.
      await page.goto("/dash0/orgs/test/discovery/new?method=kubernetes");

      // Regression: the kubernetes sub-form must render so a cluster is
      // selectable (the screenshot bug showed no cluster field at all).
      await expect(page.locator("#k8s-cluster")).toBeVisible();

      // Pick the cluster we just created (explicit — robust to other clusters
      // already existing in the shared test org).
      await page.locator("#k8s-cluster").click();
      await page.getByRole("option", { name: clusterName }).click();

      const startButton = page.getByRole("button", { name: /start scan/i });
      // With a cluster selected, submission is gated only by the confirm box.
      await expect(startButton).toBeDisabled();
      await page.getByRole("checkbox").check();

      // Regression: with a cluster available+selected and confirm checked,
      // Start scan must be ENABLED (previously it stayed disabled on Kubernetes).
      await expect(startButton).toBeEnabled();
    } finally {
      // 3. Remove the connection so the CI/other tests still see "no cluster".
      const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
        data: { org: "test", email: "test@test.com", password: "test" },
      });
      const token = (await resp.json()).accessToken as string;
      await page.request.delete(
        `${API_BASE}/api/v1/orgs/test/integrations/${clusterUid}`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
    }
  });
});
