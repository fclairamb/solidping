import { test, expect, type Page, API_BASE } from "./fixtures";

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  const body = await resp.json();
  return body.accessToken;
}

async function deleteConnection(page: Page, token: string, uid: string) {
  await page.request.delete(`${API_BASE}/api/v1/orgs/test/integrations/${uid}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
}

test.describe("Kubernetes cluster connections", () => {
  test("kubernetes appears in the data-sources picker group", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/integrations/new");
    await page.waitForLoadState("networkidle");

    const sourceGroup = page.getByTestId("group-source");
    const notifyGroup = page.getByTestId("group-notify");

    // Kubernetes is a data source — it sits in the sources group, not notify.
    await expect(sourceGroup.getByTestId("pick-kubernetes")).toBeVisible();
    await expect(notifyGroup.getByTestId("pick-kubernetes")).toHaveCount(0);
  });

  test("create a token-auth cluster, test connection, delete", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // 1. Open the kubernetes create form via the picker.
    await page.goto("orgs/test/integrations/new?type=kubernetes");
    await page.waitForLoadState("networkidle");

    // The kubernetes panel renders; default auth mode is token.
    await expect(page.getByTestId("kubernetes-panel")).toBeVisible();
    await expect(page.getByTestId("kubernetes-api-server")).toBeVisible();

    const clusterName = `E2E Cluster ${Date.now()}`;
    await page.getByLabel("Name").fill(clusterName);
    await page
      .getByTestId("kubernetes-api-server")
      .fill("https://10.0.0.1:6443");
    await page.getByTestId("kubernetes-token").fill("super-secret-sa-token");

    await page.getByRole("button", { name: /create integration/i }).click();

    // Land on the detail page (UUID path).
    await page.waitForURL((url) =>
      /\/integrations\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(
        url.pathname,
      ),
    );
    const clusterUid = page.url().split("/").pop()!;

    // Everything from here on must guarantee cleanup even if an assertion
    // throws mid-test: this test creates a REAL kubernetes integration in
    // the shared "test" org via the UI, and other specs (e.g.
    // discovery.spec.ts's "Kubernetes method option is hidden when no
    // cluster connection exists") assert that org has none. A failure
    // partway through — before the UI delete step below — used to abort the
    // test and leak the connection, which then intermittently failed that
    // unrelated discovery.spec.ts assertion whenever it ran later against
    // the same server/DB (see spec 2026-08-06-01, resolved open question 2).
    try {
      // 2. The public apiServer setting is stored and queryable. (The token is
      //    split into the encrypted private side when encryption is enabled —
      //    covered by the backend unit test TestCreateKubernetesConnectionEncryptsToken;
      //    the e2e server may run with encryption disabled, so we assert only the
      //    always-true public side here.)
      const detail = await page.request
        .get(`${API_BASE}/api/v1/orgs/test/integrations/${clusterUid}`, {
          headers: { Authorization: `Bearer ${token}` },
        })
        .then((r) => r.json());
      expect(detail.type).toBe("kubernetes");
      expect(detail.settings?.apiServer).toBe("https://10.0.0.1:6443");

      // 3. Test connection (mock the probe so it never hits a real cluster).
      await page.waitForLoadState("networkidle");
      await page.route(
        `**/api/v1/orgs/test/integrations/${clusterUid}/test`,
        async (route) => {
          if (route.request().method() === "POST") {
            await route.fulfill({
              status: 200,
              contentType: "application/json",
              body: JSON.stringify({
                success: true,
                statusCode: 200,
                durationMs: 34,
                detail: "Connected to cluster v1.30.0",
              }),
            });
            return;
          }
          await route.continue();
        },
      );

      await page.getByTestId("kubernetes-send-test").click();
      const badge = page.getByTestId("kubernetes-test-result");
      await expect(badge).toBeVisible();
      await expect(badge).toContainText("v1.30.0");

      // 4. Delete via the UI.
      await page.getByRole("button", { name: /delete integration/i }).click();
      const dialog = page.getByRole("alertdialog");
      await expect(dialog).toBeVisible();
      await dialog.getByRole("button", { name: /delete/i }).click();
      await page.waitForURL((url) => url.pathname.endsWith("/integrations"));
    } finally {
      // Defensive cleanup if the UI delete somehow didn't land, or an
      // earlier assertion threw before reaching it.
      await deleteConnection(page, token, clusterUid);
    }
  });

  test("in-cluster auth mode hides credential fields", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/integrations/new?type=kubernetes");
    await page.waitForLoadState("networkidle");

    // Switch the auth mode select to in-cluster.
    await page.getByTestId("kubernetes-auth-mode").click();
    await page.getByRole("option", { name: /in-cluster/i }).click();

    // The in-cluster hint shows; the apiServer/token fields disappear.
    await expect(page.getByTestId("kubernetes-incluster-hint")).toBeVisible();
    await expect(page.getByTestId("kubernetes-api-server")).toHaveCount(0);
    await expect(page.getByTestId("kubernetes-token")).toHaveCount(0);
  });
});
