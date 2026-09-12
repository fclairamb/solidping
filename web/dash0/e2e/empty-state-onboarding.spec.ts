import { test, expect, API_BASE, type Page, DASH_BASE } from "./fixtures";

// Covers the empty-state onboarding hero (EmptyStateOnboarding, rendered on
// /orgs/$org when the org has zero checks) and specifically the 2026-07-11
// "MCP / AI path" addition: alongside the HTTP/Ping/SSL quick-create chips
// and the full-editor link, the hero now offers a secondary "let AI set
// everything up" sub-card linking to the per-client MCP setup page under
// Account.
//
// The shared `test` org accumulates checks from other suites (and parallel
// workers), so instead of mutating shared state to empty it, both check
// endpoints the dashboard reads are stubbed empty: the list (`{"data":[]}`)
// and the aggregate counters (`total: 0`). `isEmptyOrg` in dashboard-page.tsx
// keys off the STATS endpoint — counting the list page would be wrong past
// 100 checks (GitHub issue #172) — so stubbing the list alone would no longer
// render the hero. The click-through to the MCP page then runs against the
// real backend (the MCP page needs no checks and no PAT — exactly the
// brand-new-org situation the spec calls out).
test.describe("Empty-state onboarding (zero-checks dashboard hero)", () => {
  async function gotoEmptyDashboard(page: import("./fixtures").Page) {
    // Needs its own pattern: a single `*` in a Playwright URL glob never
    // matches `/`, so the `checks*` route below cannot serve `/checks/stats`.
    await page.route("**/api/v1/orgs/test/checks/stats*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          total: 0,
          enabled: 0,
          disabled: 0,
          byStatus: {},
          down: 0,
          hardDown: 0,
        }),
      }),
    );
    await page.route("**/api/v1/orgs/test/checks*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ data: [] }),
      }),
    );
    await page.goto("orgs/test");
    await page.waitForLoadState("networkidle");
  }

  test("offers quick-create, the MCP / AI path, and the full editor", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoEmptyDashboard(page);

    // Primary path: the three quick-start chips and the one-field form.
    await expect(page.getByTestId("quick-start-http")).toBeVisible();
    await expect(page.getByTestId("quick-start-icmp")).toBeVisible();
    await expect(page.getByTestId("quick-start-ssl")).toBeVisible();
    await expect(page.getByTestId("quick-start-input")).toBeVisible();
    await expect(page.getByTestId("quick-start-submit")).toBeVisible();

    // Secondary path: the MCP / AI sub-card links to the Account MCP page.
    const mcpLink = page.getByTestId("quick-start-mcp-link");
    await expect(mcpLink).toBeVisible();
    const href = await mcpLink.getAttribute("href");
    expect(href).toBeTruthy();
    expect(new URL(href!, page.url()).pathname).toBe(
      `${DASH_BASE}/orgs/test/account/mcp`,
    );

    // Tertiary path: the full check editor hint is still there.
    await expect(
      page.locator(`a[href="${DASH_BASE}/orgs/test/checks/new"]`),
    ).toBeVisible();
  });

  test("MCP CTA click-through lands on a working MCP setup page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoEmptyDashboard(page);

    await page.getByTestId("quick-start-mcp-link").click();
    await page.waitForURL(/\/orgs\/test\/account\/mcp/);

    // The MCP page renders fully for an org with zero checks and no PAT:
    // heading plus the instance MCP URL derived from the origin.
    await expect(
      page.getByRole("heading", { name: /ai assistants/i }),
    ).toBeVisible();
    const origin = await page.evaluate(() => window.location.origin);
    await expect(page.getByText(`${origin}/api/v1/mcp`).first()).toBeVisible();
  });

  test("hero stays usable at mobile width, quick-create first", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 812 });
    await gotoEmptyDashboard(page);

    await expect(page.getByTestId("quick-start-input")).toBeVisible();
    await expect(page.getByTestId("quick-start-mcp-link")).toBeVisible();

    // No fixed-width element may force the page wider than the viewport.
    const hasHorizontalOverflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    );
    expect(hasHorizontalOverflow).toBe(false);

    // Hierarchy: the quick-create form renders above the MCP sub-card.
    const formBox = await page.getByTestId("quick-start-input").boundingBox();
    const mcpBox = await page.getByTestId("quick-start-mcp-link").boundingBox();
    expect(formBox).toBeTruthy();
    expect(mcpBox).toBeTruthy();
    expect(formBox!.y).toBeLessThan(mcpBox!.y);

    // Spec 2026-09-12-04: the validation message the now-always-enabled submit
    // produces is the widest string this hero can render — it must wrap inside
    // the phone viewport rather than push the page sideways.
    await page.getByTestId("quick-start-submit").click();
    await expect(page.getByTestId("quick-start-error")).toBeVisible();
    const stillNoOverflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    );
    expect(stillNoOverflow).toBe(false);
  });

  // Spec 2026-09-12-04: the hero's three activation defects — chips that
  // wiped the input, nothing that ever moved the caret, and a dead disabled
  // submit. These run against the stubbed-empty `test` org because none of
  // them POSTs a check (the validation ones fail client-side on purpose); the
  // SSL create that DOES post lives in the real-empty-org block below.
  test("focuses the quick-create input on mount and on every chip click", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoEmptyDashboard(page);

    // On mount: the desktop Playwright browser reports `(pointer: fine)`, so
    // the conditional mount-focus applies.
    await expect(page.getByTestId("quick-start-input")).toBeFocused();

    // Click somewhere inert to move the caret away, then prove each chip
    // brings it back. Pre-fix the chip handler touched no ref at all, so the
    // focus stayed on the chip button.
    for (const chip of ["icmp", "ssl", "http"] as const) {
      await page.getByRole("heading", { name: /welcome/i }).click();
      await expect(page.getByTestId("quick-start-input")).not.toBeFocused();
      await page.getByTestId(`quick-start-${chip}`).click();
      await expect(page.getByTestId("quick-start-input")).toBeFocused();
    }
  });

  test("a hostname survives every chip switch, a URL is cleared only where it cannot apply", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoEmptyDashboard(page);
    const input = page.getByTestId("quick-start-input");

    // A bare hostname is a valid target for HTTP, Ping AND SSL, so it must
    // survive the whole round trip. Pre-fix, `setValue("")` ran on every one
    // of these clicks and each assertion below would read "".
    await input.fill("example.com");
    await page.getByTestId("quick-start-ssl").click();
    await expect(input).toHaveValue("example.com");
    await page.getByTestId("quick-start-icmp").click();
    await expect(input).toHaveValue("example.com");
    await page.getByTestId("quick-start-http").click();
    await expect(input).toHaveValue("example.com");

    // A full URL still applies to HTTP...
    await input.fill("https://acme.com/health");
    await page.getByTestId("quick-start-ssl").click();
    // ...but not to a host-based type: `https://acme.com/health` can never be
    // an SSL host or a ping target, so clearing it is correct. Without this
    // negative case the test above would also pass against a naive "never
    // clear anything" implementation.
    await expect(input).toHaveValue("");

    await input.fill("https://acme.com/health");
    await page.getByTestId("quick-start-icmp").click();
    await expect(input).toHaveValue("");
  });

  test("the submit is never a dead disabled button: it explains what is missing", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await gotoEmptyDashboard(page);
    const input = page.getByTestId("quick-start-input");
    const submit = page.getByTestId("quick-start-submit");

    // Empty input: enabled (pre-fix it was `disabled` and clicking it was a
    // silent no-op).
    await expect(input).toHaveValue("");
    await expect(submit).toBeEnabled();

    await submit.click();
    const error = page.getByTestId("quick-start-error");
    await expect(error).toBeVisible();
    await expect(error).toContainText(/url or hostname/i);
    // Announced, not just painted: the message sits inside the role="alert"
    // banner and the field is marked invalid and described by it.
    await expect(page.locator('[role="alert"]', { has: error })).toBeVisible();
    await expect(input).toHaveAttribute("aria-invalid", "true");
    await expect(input).toHaveAttribute("aria-describedby", "quick-input-error");
    // Focus lands back where the fix has to happen.
    await expect(input).toBeFocused();

    // A value that cannot apply to the selected type gets its own message
    // rather than a silent no-op or an opaque server error.
    await page.getByTestId("quick-start-icmp").click();
    await input.fill("https://acme.com/health");
    await submit.click();
    await expect(error).toContainText(/no http:\/\//i);

    // Typing clears the message again.
    await input.fill("acme.com");
    await expect(error).not.toBeVisible();
    await expect(input).not.toHaveAttribute("aria-invalid", "true");
  });

  // Spec 2026-08-29-07: submitting the quick-create form used to just clear
  // the input and let the dashboard re-render into the regular view once the
  // checks list refetched. It now navigates straight to the new check's own
  // page instead — the moment of highest engagement right after creating a
  // first check. This needs a genuinely empty org (not the shared `test` org
  // stubbed empty above): the earlier tests' route stubs intercept every
  // request to `**/api/v1/orgs/test/checks*`, method included, so a real
  // quick-create POST through them would be swallowed by the `{data: []}`
  // GET stub instead of returning the created check. A fresh org sidesteps
  // that and also gives a clean, unshared checks list to assert against.
  test.describe("quick-create redirect (needs a real empty org)", () => {
    /** Creates a fresh org with zero checks, authenticated as its owner. */
    async function seedEmptyOrg(page: Page): Promise<string> {
      const stamp = Date.now() + Math.floor(Math.random() * 1000);
      const email = `quickcreate-${stamp}@unknown.example`;
      const password = "Strong-Pass-123!";

      const createUserResp = await page.request.post(
        `${API_BASE}/api/v1/test/users`,
        { data: { email, password, name: "Quick Create Owner" } },
      );
      if (createUserResp.status() !== 201) {
        test.skip(
          true,
          `test user-seed endpoint unavailable (server not in SP_RUNMODE=test?): ${createUserResp.status()}`,
        );
      }

      const loginResp = await page.request.post(
        `${API_BASE}/api/v1/auth/login`,
        { data: { email, password } },
      );
      expect(loginResp.status()).toBe(200);
      const session = (await loginResp.json()) as { accessToken: string };

      const slug = `qc-${stamp.toString(36)}`;
      const createOrgResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
        headers: { Authorization: `Bearer ${session.accessToken}` },
        data: { name: `Acme Quick Create ${stamp}`, slug },
      });
      expect(createOrgResp.status()).toBe(201);
      const org = (await createOrgResp.json()) as {
        slug: string;
        accessToken: string;
        refreshToken?: string;
        expiresIn?: number;
      };

      await page.addInitScript(
        ({ accessToken, refreshToken, expiresIn, orgSlug }) => {
          localStorage.setItem("solidping_session_token", accessToken as string);
          if (refreshToken) {
            localStorage.setItem(
              "solidping_refresh_token",
              refreshToken as string,
            );
          }
          if (expiresIn) {
            localStorage.setItem(
              "solidping_expires_at",
              String(Date.now() + Number(expiresIn) * 1000),
            );
            localStorage.setItem("solidping_expires_in", String(expiresIn));
          }
          localStorage.setItem("solidping_org", orgSlug as string);
        },
        {
          accessToken: org.accessToken,
          refreshToken: org.refreshToken ?? "",
          expiresIn: org.expiresIn ?? 0,
          orgSlug: org.slug,
        },
      );

      return org.slug;
    }

    test("navigates to the new check's page, and the hero is gone on return", async ({
      page,
    }) => {
      const orgSlug = await seedEmptyOrg(page);

      await page.goto(`orgs/${orgSlug}`);
      await page.waitForLoadState("networkidle");

      // Confirms the "dashboard switches out of the empty state" coverage
      // this test replaces: the hero is present before create.
      await expect(page.getByTestId("quick-start-input")).toBeVisible();

      await page.getByTestId("quick-start-input").fill("https://acme.com");
      await page.getByTestId("quick-start-submit").click();

      // Lands on the check detail route, not back on the dashboard.
      await page.waitForURL(
        new RegExp(`/orgs/${orgSlug}/checks/[^/]+/?$`),
      );
      await expect(
        page.locator('[data-testid="check-detail-header"] h1'),
      ).toContainText("HTTP — acme.com");

      // Navigating back to the dashboard: the org now has a check, so the
      // empty-state hero must be gone (replaced by the regular dashboard /
      // onboarding-checklist view, with its KPI tiles) — the behavior the
      // old assertion covered.
      //
      // `isEmptyOrg` (dashboard-page.tsx) keys off GET checks/stats. Before
      // spec 2026-09-01-01, that endpoint's per-org server cache (server/
      // internal/handlers/checks/stats.go) had deliberately no invalidation
      // machinery, so the dashboard visit above would have primed it with
      // `total: 0` and this reload — a fresh page load with its own
      // QueryClient, so the client-side fix in useCreateCheck/LiveEventsContext
      // does not even apply here — would have kept reading that stale zero
      // for up to a minute. This is now run WITHOUT a route stub: the
      // server-side cache bust in Service.insertCheckResolvingSlugRace
      // (called by the quick-create POST above) must have already dropped
      // that cache entry, so this real, unstubbed reload sees the check
      // immediately.
      await page.goto(`orgs/${orgSlug}`);
      await page.waitForLoadState("networkidle");
      await expect(page.getByTestId("quick-start-input")).not.toBeVisible();
      await expect(page.getByTestId("quick-start-submit")).not.toBeVisible();

      // Not just "hero gone" — the KPI tiles that replace it must actually
      // render, proving the dashboard switched all the way into its normal
      // (non-empty) mode rather than into some blank in-between state.
      await expect(page.getByTestId("kpi-tile-monitored")).toBeVisible();
      await expect(page.getByTestId("kpi-tile-monitored")).toContainText(
        "1 Active",
      );
    });

    // Spec 2026-09-12-04: the SSL chip posted `config.domain`, but
    // checkssl.SSLConfig.FromMap reads only `host`, so the key was dropped and
    // the create came back 400 "host is required" — one of the hero's three
    // chips could never produce a check for anybody. Asserting through the
    // real API is the only way to catch this: a stubbed POST would have
    // happily accepted the wrong key.
    test("the SSL chip actually creates an SSL check from a bare domain", async ({
      page,
    }) => {
      const orgSlug = await seedEmptyOrg(page);

      await page.goto(`orgs/${orgSlug}`);
      await page.waitForLoadState("networkidle");

      await page.getByTestId("quick-start-ssl").click();
      await page.getByTestId("quick-start-input").fill("acme.com");
      await page.getByTestId("quick-start-submit").click();

      await page.waitForURL(new RegExp(`/orgs/${orgSlug}/checks/[^/]+/?$`));
      await expect(
        page.locator('[data-testid="check-detail-header"] h1'),
      ).toContainText("SSL — acme.com");
      // No error banner survived on the way out — i.e. this is a real create,
      // not a navigation that happened to race a rejection.
      await expect(page.getByTestId("quick-start-error")).toHaveCount(0);
    });

    // A bare hostname must also work for HTTP even though the backend insists
    // on a scheme (checkhttp.Validate: "must start with http:// or https://"):
    // the hero promotes it to https:// on submit. Pre-fix the input was
    // type="url" + required, so a scheme-less value never reached the submit
    // handler at all — the browser silently refused the form.
    test("a scheme-less hostname creates an HTTP check over https", async ({
      page,
    }) => {
      const orgSlug = await seedEmptyOrg(page);

      await page.goto(`orgs/${orgSlug}`);
      await page.waitForLoadState("networkidle");

      await page.getByTestId("quick-start-input").fill("acme.com");
      await page.getByTestId("quick-start-submit").click();

      await page.waitForURL(new RegExp(`/orgs/${orgSlug}/checks/[^/]+/?$`));
      await expect(
        page.locator('[data-testid="check-detail-header"] h1'),
      ).toContainText("HTTP — acme.com");
      await expect(page.getByText("https://acme.com").first()).toBeVisible();
    });
  });
});
