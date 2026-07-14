import { test, expect, type Page } from "./fixtures";

async function navigateToServer(page: Page) {
  await page.getByTestId("user-menu-button").click();
  await page.getByTestId("server-settings-link").click();
  await page.waitForURL(/\/server\/web/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");
}

test.describe("Server Admin", () => {
  test("should show Server link in user menu for superadmin", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Open user dropdown
    await page.getByTestId("user-menu-button").click();

    // Superadmin user should see the "Server" link in the dropdown
    await expect(page.getByTestId("server-settings-link")).toBeVisible();
  });

  test("should navigate to server settings and display web tab", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Navigate via user menu
    await navigateToServer(page);

    // Verify the page heading
    await expect(
      page.getByRole("heading", { name: "Server Settings" }),
    ).toBeVisible();

    // Verify all tabs are visible
    await expect(page.getByRole("link", { name: "Web" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Mail", exact: true })).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Authentication" }),
    ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Performance" }),
    ).toBeVisible();

    // Verify web settings content
    await expect(page.getByLabel("Base URL")).toBeVisible();
    await expect(page.getByText("JWT Secret")).toBeVisible();
    await expect(page.getByRole("button", { name: "Save" })).toBeVisible();
  });

  test("should navigate between server tabs", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Navigate via user menu
    await navigateToServer(page);

    // Click Mail tab
    await page.getByRole("link", { name: "Mail", exact: true }).click();
    await page.waitForURL(/\/server\/mail/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByLabel("SMTP Host")).toBeVisible();
    await expect(page.getByLabel("From Address")).toBeVisible();

    // Click Authentication tab
    await page.getByRole("link", { name: "Authentication" }).click();
    await page.waitForURL(/\/server\/auth/);
    await page.waitForLoadState("networkidle");
    await expect(page.getByText("Client ID", { exact: true }).first()).toBeVisible();
  });

  test("should save base URL setting", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Navigate via user menu
    await navigateToServer(page);

    // Fill in the base URL
    const baseUrlInput = page.getByLabel("Base URL");
    await baseUrlInput.fill("https://solidping.example.com");

    // Click Save
    await page.getByRole("button", { name: "Save" }).click();

    // Wait for success message
    await expect(page.getByText("Settings saved.")).toBeVisible({
      timeout: 10000,
    });
  });

  test("should display test email section on mail tab", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.waitForLoadState("networkidle");

    // Navigate to server, then mail tab
    await navigateToServer(page);
    await page.getByRole("link", { name: "Mail", exact: true }).click();
    await page.waitForURL(/\/server\/mail/);
    await page.waitForLoadState("networkidle");

    // Verify test email section is visible
    await expect(page.getByLabel("Recipient Email")).toBeVisible();
    await expect(page.getByTestId("send-test-email-button")).toBeVisible();
  });
});

// navigateToHashing opens Server Settings and lands on the Password Hashing tab.
async function navigateToHashing(page: Page) {
  await navigateToServer(page);
  await page.getByRole("link", { name: "Password Hashing" }).click();
  await page.waitForURL(/\/server\/hashing/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");
}

// selectAlgorithm switches the Radix algorithm Select to the given option and
// waits for the matching cost inputs to render. Each test sets the algorithm it
// needs explicitly rather than relying on the persisted value, since these tests
// write the shared auth.password.* system parameters.
async function selectAlgorithm(page: Page, name: "argon2id" | "bcrypt") {
  await page.getByTestId("hashing-algorithm").click();
  // The option label includes a suffix for argon2id ("argon2id (recommended)"),
  // so match by the leading id.
  await page.getByRole("option", { name, exact: false }).first().click();
}

// These tests persist the shared auth.password.* system parameters on a single
// side-car server, so they must run serially to avoid stomping each other.
test.describe.configure({ mode: "serial" });

test.describe("Server Admin — Password Hashing", () => {
  test("super-admin can open the Hashing tab and sees the restart notice", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    await navigateToServer(page);
    await expect(
      page.getByRole("link", { name: "Password Hashing" }),
    ).toBeVisible();

    await navigateToHashing(page);

    // The algorithm select and the restart notice must be present.
    await expect(page.getByTestId("hashing-algorithm")).toBeVisible();
    await expect(page.getByText(/take effect after a server restart/i)).toBeVisible();
  });

  test("argon2 preset fills the cost inputs", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");
    await navigateToHashing(page);

    // Explicitly select argon2id so the preset buttons are shown regardless of
    // any previously-persisted algorithm.
    await selectAlgorithm(page, "argon2id");

    await expect(page.getByTestId("hashing-preset-owasp")).toBeVisible();
    await page.getByTestId("hashing-preset-owasp").click();

    // The OWASP profile is 19 MiB / t2 / p1.
    await expect(page.getByTestId("hashing-argon2-memory")).toHaveValue("19456");
    await expect(page.getByTestId("hashing-argon2-time")).toHaveValue("2");
    await expect(page.getByTestId("hashing-argon2-threads")).toHaveValue("1");
  });

  test("out-of-range bcrypt cost is rejected before saving", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");
    await navigateToHashing(page);

    await selectAlgorithm(page, "bcrypt");

    const cost = page.getByTestId("hashing-bcrypt-cost");
    await expect(cost).toBeVisible();
    // 99 is outside the accepted [10,31] range. The numeric input carries the
    // same min/max bounds the backend enforces, so the browser blocks the submit
    // with a native validity error and no save round-trips. (The API layer's 422
    // for the same value is covered by the Go handler test
    // TestSetParameterValidatesPasswordParams.)
    await cost.fill("99");

    // The input reports invalid against its max bound.
    const validity = await cost.evaluate((el: HTMLInputElement) => ({
      valid: el.checkValidity(),
      max: el.max,
    }));
    expect(validity.valid).toBe(false);
    expect(validity.max).toBe("31");

    // Clicking save does not produce a saved confirmation (the submit is blocked).
    await page.getByTestId("hashing-save").click();
    await expect(page.getByTestId("hashing-saved")).toHaveCount(0);
  });

  test("switching to bcrypt cost 12 saves with a confirmation", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");
    await navigateToHashing(page);

    await selectAlgorithm(page, "bcrypt");

    // The bcrypt cost input appears; set a valid cost.
    const cost = page.getByTestId("hashing-bcrypt-cost");
    await expect(cost).toBeVisible();
    await cost.fill("12");

    await page.getByTestId("hashing-save").click();

    await expect(page.getByTestId("hashing-saved")).toBeVisible({ timeout: 10000 });
  });

  test("server-side 422 validation surfaces when the client guard is bypassed", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");
    await navigateToHashing(page);

    await selectAlgorithm(page, "bcrypt");

    const cost = page.getByTestId("hashing-bcrypt-cost");
    await expect(cost).toBeVisible();

    // Strip the native max bound so the out-of-range value passes the browser's
    // client-side validity check and actually round-trips to the API. This is the
    // path that proves the server-side validation (422 VALIDATION_ERROR) reaches
    // the user, not just the native min/max guard.
    await cost.evaluate((el: HTMLInputElement) => el.removeAttribute("max"));
    await cost.fill("99"); // outside the accepted [10,31] range

    // Capture the PUT to the bcrypt-cost parameter so we can assert the API itself
    // returns 422, independent of the UI rendering.
    const responsePromise = page.waitForResponse(
      (res) =>
        res.url().includes("/api/v1/system/parameters/auth.password.bcrypt.cost") &&
        res.request().method() === "PUT",
      { timeout: 10000 },
    );

    await page.getByTestId("hashing-save").click();

    const response = await responsePromise;
    expect(response.status()).toBe(422);

    // The UI surfaces the API validation error (no saved confirmation).
    await expect(page.getByTestId("hashing-error")).toBeVisible({ timeout: 10000 });
    await expect(page.getByTestId("hashing-saved")).toHaveCount(0);
  });
});

// navigateToAuth opens Server Settings and lands on the Authentication tab.
async function navigateToAuth(page: Page) {
  await navigateToServer(page);
  await page.getByRole("link", { name: "Authentication" }).click();
  await page.waitForURL(/\/server\/auth/, { timeout: 10000 });
  await page.waitForLoadState("networkidle");
}

// The Microsoft Tenant ID field is a non-secret system parameter
// (auth.microsoft.tenant_id). These tests write that shared parameter on a single
// side-car server, so they run serially (the suite is already configured serial
// above) to avoid stomping each other.
test.describe("Server Admin — Microsoft Tenant ID", () => {
  const tenantField = "provider-field-auth.microsoft.tenant_id";

  test("Authentication tab shows the Microsoft Tenant ID field with help and common placeholder", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    await navigateToAuth(page);

    // The Microsoft card carries a Tenant ID label and a plain-text (non-secret)
    // input defaulting its placeholder to "common".
    await expect(page.getByText("Tenant ID", { exact: true })).toBeVisible();

    const input = page.getByTestId(tenantField);
    await expect(input).toBeVisible();
    await expect(input).toHaveAttribute("placeholder", "common");
    // Non-secret -> plain text input, not a password field.
    await expect(input).toHaveAttribute("type", "text");

    // The per-field help line documenting the accepted tenant forms is rendered.
    await expect(
      page.getByText(/Entra directory \(tenant\) ID/i),
    ).toBeVisible();
  });

  test("entering a tenant marks the Microsoft card dirty and Save persists it across reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");

    await navigateToAuth(page);

    const input = page.getByTestId(tenantField);
    await expect(input).toBeVisible();

    const tenant = "11111111-2222-3333-4444-555555555555";
    await input.fill(tenant);

    // The Microsoft card reports unsaved changes once the value diverges.
    await expect(page.getByTestId("provider-dirty-microsoft")).toBeVisible();

    // Save the Microsoft card. Its Save button is the one inside the Microsoft
    // card; scope the click to that card to avoid the other providers' buttons.
    const microsoftCard = page
      .locator("div")
      .filter({ has: page.getByTestId(tenantField) })
      .filter({ hasText: "Microsoft" })
      .last();
    await microsoftCard.getByRole("button", { name: "Save" }).click();

    await expect(page.getByText("Settings saved.")).toBeVisible({
      timeout: 10000,
    });

    // Reload and confirm the non-secret value round-trips from GET
    // /system/parameters (it is not masked).
    await page.reload();
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId(tenantField)).toHaveValue(tenant);
  });
});

// A non-super-admin who reaches the Server Settings area (here, the Hashing tab)
// must be redirected away by the route guard in server.tsx, never shown the
// settings. We can't log in as a non-super-admin through the seeded test user
// (it is a super-admin), so we stub /auth/me to return a plain admin and assert
// the guard bounces us back to the org dashboard.
test.describe("Server Admin — non-super-admin guard", () => {
  test("non-super-admin is redirected away from the Hashing route", async ({
    page,
  }) => {
    // Return a non-super-admin (role: "admin") from /auth/me so AuthContext sets
    // isSuperAdmin=false.
    await page.route("**/api/v1/auth/me", (route) =>
      route.fulfill({
        status: 200,
        body: JSON.stringify({
          user: { email: "member@example.com", role: "admin" },
          organization: { slug: "test" },
          organizations: [{ slug: "test", name: "Test" }],
        }),
      }),
    );

    // Pre-set a token so AuthContext attempts to resolve the (stubbed) session.
    await page.addInitScript(() => {
      window.localStorage.setItem("solidping_token", "stub");
    });

    await page.goto("orgs/test/server/hashing");
    await page.waitForLoadState("networkidle");

    // The guard navigates back to /orgs/test (replace), dropping /server.
    await page.waitForURL((url) => !url.pathname.includes("/server"), {
      timeout: 10000,
    });
    expect(page.url()).not.toContain("/server");

    // The hashing form must never have rendered.
    await expect(page.getByTestId("hashing-algorithm")).toHaveCount(0);
  });
});

test.describe("Server Performance", () => {
  test("check-runner settings are grouped and the fast-lane floor is validated against the pool size", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/server/performance");
    await page.waitForLoadState("networkidle");

    // The check-runner pool and its fast-lane floor render as one group,
    // background jobs as another.
    await expect(
      page.getByRole("heading", { name: /check runners/i }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: /background jobs/i }),
    ).toBeVisible();

    const pool = page.locator("#checkWorkers");
    const floor = page.getByTestId("fast-lane-reserved-input");
    await expect(pool).toBeVisible();

    // A floor >= pool size is rejected client-side: inline error + save
    // disabled (the backend would silently clamp it otherwise).
    await pool.fill("10");
    await floor.fill("10");
    await expect(page.getByTestId("fast-lane-reserved-error")).toBeVisible();
    await expect(page.getByRole("button", { name: /save/i })).toBeDisabled();

    // Back inside the bound, the error clears and save re-arms.
    await floor.fill("4");
    await expect(page.getByTestId("fast-lane-reserved-error")).toHaveCount(0);
    await expect(page.getByRole("button", { name: /save/i })).toBeEnabled();
  });

  test("current-load card reports fast and slow lanes per worker", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/server/performance");
    await page.waitForLoadState("networkidle");

    // The lane-load report renders one table per worker with a fast and a
    // slow row (the test server registers its own worker at startup).
    await expect(
      page.getByRole("heading", { name: /current load/i }),
    ).toBeVisible();
    await expect(page.getByTestId("lane-load-fast").first()).toBeVisible();
    await expect(page.getByTestId("lane-load-slow").first()).toBeVisible();

    // The worker's region badge shows the resolved "{emoji} {name}" label
    // from the region definitions — with no regions parameter set, the
    // built-in default region resolves to "📍 Default" — never the raw
    // "default" slug.
    await expect(page.getByTestId("worker-region-badge").first()).toHaveText(
      "📍 Default",
    );
  });
});

// The Aggregation tab writes the shared performance.aggregation_retention_*
// system parameters; the file is already configured serial above, so these
// don't stomp each other or the other parameter-writing suites.
test.describe("Server Aggregation", () => {
  test("tab shows three unit-labelled retention fields and the behaviour notes", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/server/aggregation");
    await page.waitForLoadState("networkidle");

    // Each tier's field is labelled with its unit (hours / days / months).
    await expect(page.getByLabel("Raw retention (hours)")).toBeVisible();
    await expect(page.getByLabel("Hourly retention (days)")).toBeVisible();
    await expect(page.getByLabel("Daily retention (months)")).toBeVisible();

    // The two decision-2 behaviours are surfaced inline so the compaction
    // semantics aren't a surprise.
    await expect(page.getByText(/never re-processes data/i)).toBeVisible();
    await expect(
      page.getByText(/does not restore data that was already rolled up/i),
    ).toBeVisible();
  });

  test("navigating via the Aggregation tab lands on the retention form", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.waitForLoadState("networkidle");
    await navigateToServer(page);

    await page.getByRole("link", { name: "Aggregation" }).click();
    await page.waitForURL(/\/server\/aggregation/, { timeout: 10000 });
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("aggregation-raw-hours-input")).toBeVisible();
  });

  test("editing and saving persists across reload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/server/aggregation");
    await page.waitForLoadState("networkidle");

    // Set explicit, valid windows so the test is deterministic regardless of
    // any previously-persisted value.
    await page.getByTestId("aggregation-raw-hours-input").fill("24");
    await page.getByTestId("aggregation-hour-days-input").fill("14");
    await page.getByTestId("aggregation-day-months-input").fill("3");

    await page.getByTestId("aggregation-save").click();
    await expect(page.getByTestId("aggregation-saved")).toBeVisible({
      timeout: 10000,
    });

    // The non-secret values round-trip from GET /system/parameters.
    await page.reload();
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("aggregation-hour-days-input")).toHaveValue(
      "14",
    );
    await expect(page.getByTestId("aggregation-day-months-input")).toHaveValue(
      "3",
    );
  });

  test("a below-floor window is rejected client-side (save disabled, no save)", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.goto("orgs/test/server/aggregation");
    await page.waitForLoadState("networkidle");

    // 0 is below the floor of 1; the field flags an inline error and the Save
    // button disarms so the invalid value never round-trips.
    await page.getByTestId("aggregation-hour-days-input").fill("0");
    await expect(page.getByText(/whole number of at least 1/i)).toBeVisible();
    await expect(page.getByTestId("aggregation-save")).toBeDisabled();
    await expect(page.getByTestId("aggregation-saved")).toHaveCount(0);
  });
});
