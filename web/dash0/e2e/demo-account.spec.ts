import { test, expect } from "@playwright/test";
import { API_BASE } from "./fixtures";

/**
 * The shared public live demo (spec 2026-09-06-02).
 *
 * These run against a test-mode server, which enables the demo unconditionally
 * (SP_RUN_MODE=test forces `demo.enabled`, see config.Load) so the whole flow —
 * the login button, the persistent banner, a real check create, and the
 * read-only refusals — is exercisable without a second server configuration.
 *
 * Deliberately NOT using the `authenticatedPage` fixture: every test here is
 * about the demo session specifically, so each drives its own login.
 */

/** Reads the instance's public config, which is what the login button keys on. */
async function demoConfig(request: import("@playwright/test").APIRequestContext) {
  const response = await request.get(`${API_BASE}/api/v1/config`);
  expect(response.ok()).toBeTruthy();

  const body = await response.json();

  return body.demo as
    | { enabled: boolean; orgSlug?: string; email?: string; password?: string }
    | undefined;
}

test.describe("Public live demo", () => {
  test("the instance advertises a demo in test mode", async ({ request }) => {
    const demo = await demoConfig(request);

    expect(demo?.enabled).toBe(true);
    expect(demo?.orgSlug).toBeTruthy();
    expect(demo?.email).toBeTruthy();
    // The password is public BY DESIGN — the whole feature is "anyone can log
    // in and look around" — so it is served rather than hardcoded in the
    // bundle. See DemoPublicConfig.
    expect(demo?.password).toBeTruthy();
  });

  test("the login page offers a one-click entry into the demo", async ({
    page,
    request,
  }) => {
    // Spec 2026-09-08-01 §D. This assertion used to be
    // `waitForURL(/\/orgs\/[^/]+/)` + the demo banner — a pattern the LOGIN
    // page's own URL already matches, and a banner `DemoBanner` renders off
    // `user.isDemo` on every org page including the wrong one. A visitor
    // stranded on /orgs/test with a demo session passed it. Name the demo org
    // and refuse the URL's org instead, exactly like the ?demo flag tests do.
    const demo = await demoConfig(request);
    const org = demo?.orgSlug as string;

    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");

    const demoButton = page.getByTestId("login-demo");
    await expect(demoButton).toBeVisible();

    await demoButton.click();

    // One click must land inside the DEMO org, banner and all.
    await page.waitForURL(new RegExp(`/orgs/${org}(/|$)`), { timeout: 20000 });
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });
    expect(page.url()).not.toContain("/orgs/test");
  });

  test("?demo=1 signs the visitor in on load", async ({ page }) => {
    // The marketing site's deep link: land in a working dashboard, not a form.
    await page.goto("orgs/test/login?demo=1");

    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });
  });

  // Spec 2026-09-07-02 §A. The flag was honoured ONLY on an org-scoped login
  // page, so every link a human would naturally write dropped it: the root
  // /login forwarded only returnTo, / redirected with no search at all, and
  // /orgs/<slug> folded the whole URL (flag included) into `returnTo`, where
  // the login page's own search params never see it.
  for (const [label, path] of [
    ["the root login page", "login?demo=true"],
    ["the dashboard root", "?demo=true"],
    ["an org page with no /login", "orgs/test?demo=true"],
  ] as const) {
    test(`?demo=true entered from ${label} lands in the demo`, async ({
      page,
      request,
    }) => {
      const demo = await demoConfig(request);
      const org = demo?.orgSlug as string;

      await page.goto(path);

      // The destination is the DEMO org, never the org the URL named.
      await page.waitForURL(new RegExp(`/orgs/${org}(/|$)`), { timeout: 20000 });
      await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });
    });
  }

  test("the demo flag beats a session in the visitor's own org", async ({
    page,
    request,
  }) => {
    const demo = await demoConfig(request);
    const org = demo?.orgSlug as string;

    // Sign in as the ordinary test user first — this is the case the
    // redirect-if-already-authenticated effect used to win, sending the
    // visitor to /orgs/test instead of into the demo.
    await page.goto("orgs/test/login");
    await page.getByTestId("login-title").waitFor({ state: "visible", timeout: 20000 });
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();
    await page.waitForURL(/\/orgs\/test(\/|$)/, { timeout: 20000 });

    await page.goto("orgs/test/login?demo=true");

    await page.waitForURL(new RegExp(`/orgs/${org}(/|$)`), { timeout: 20000 });
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });
    expect(page.url()).not.toContain("/orgs/test");
  });

  test("re-entering the demo with a demo session mints no second session", async ({
    page,
    request,
  }) => {
    const demo = await demoConfig(request);
    const org = demo?.orgSlug as string;

    await page.goto("orgs/test/login?demo=1");
    await page.waitForURL(new RegExp(`/orgs/${org}(/|$)`), { timeout: 20000 });
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });

    // Count login POSTs from here on: a valid demo session must short-circuit
    // to the demo org rather than authenticate all over again.
    const logins: string[] = [];
    page.on("request", (req) => {
      if (req.method() === "POST" && req.url().includes("/api/v1/auth/login")) {
        logins.push(req.url());
      }
    });

    await page.goto("orgs/test/login?demo=true");
    await page.waitForURL(new RegExp(`/orgs/${org}(/|$)`), { timeout: 20000 });
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });
    // Landing on the DEMO org, not on /orgs/test, is half the assertion — the
    // demo account is not a member of `test` and would get Permission Denied.
    expect(page.url()).not.toContain("/orgs/test");
    await page.waitForLoadState("networkidle");
    expect(logins, "a valid demo session must not log in again").toHaveLength(0);
  });

  test("a demo visitor can rename and then delete a check they created", async ({
    page,
    request,
  }) => {
    // Spec 2026-09-07-02 §C — the positive control the suite lacked: every
    // other demo test exercises a refusal or a create, none an EDIT of an
    // owned check. The edit PATCH always succeeded; the unconditional
    // PUT .../channels that followed it was refused with DEMO_READ_ONLY and
    // took the toast + navigation down with it, so the visitor sat on the form
    // staring at a red toast while their rename had in fact been applied.
    const demo = await demoConfig(request);
    const org = demo?.orgSlug as string;

    // §C.2 has a second, subtler instance: the escalation picker's
    // "No escalation (silent)" option is not a plain selection — when the org
    // owns no zero-step policy (and the demo org's single seeded policy has a
    // step), it POSTs one to /orgs/:org/escalation-policies, which is NOT on
    // the demo allowlist. Count those POSTs for the whole flow; the assertion
    // is that the option is never even offered, so there are none.
    const policyPosts: string[] = [];
    page.on("request", (req) => {
      if (
        req.method() === "POST" &&
        /\/api\/v1\/orgs\/[^/]+\/escalation-policies$/.test(
          new URL(req.url()).pathname,
        )
      ) {
        policyPosts.push(req.url());
      }
    });

    await page.goto("orgs/test/login?demo=1");
    await page.waitForURL(new RegExp(`/orgs/${org}(/|$)`), { timeout: 20000 });

    const name = `e2e-demo-edit-${Date.now()}`;

    await page.goto(`orgs/${org}/checks/new`);
    await page.waitForLoadState("networkidle");

    const nameField = page.getByTestId("check-name-input");
    await nameField.waitFor({ state: "visible", timeout: 20000 });
    await nameField.fill(name);
    const urlField = page.getByTestId("check-url-input");
    await urlField.waitFor({ state: "visible", timeout: 20000 });
    await urlField.fill(`${API_BASE}/api/v1/fake?period=86400`);
    await page.getByTestId("check-submit-button").click();

    await page.waitForURL(/\/checks\/[0-9a-f-]{36}/, { timeout: 30000 });
    const checkUrl = page.url();

    // Now edit it — the bug.
    await page
      .getByTestId("check-detail-header")
      .getByRole("link", { name: "Edit", exact: true })
      .click();
    await page.waitForURL(/\/checks\/[0-9a-f-]{36}\/edit/, { timeout: 20000 });
    await page.getByTestId("check-name-input").waitFor({
      state: "visible",
      timeout: 20000,
    });

    // Nothing a demo session would only be refused is offered: the channel
    // picker is replaced by the read-only note, and so is the dependency
    // editor once its section is expanded.
    await expect(page.getByTestId("check-notifications-demo-note")).toBeVisible();
    await expect(page.getByText("Notify via")).toHaveCount(0);
    await page.getByTestId("section-dependencies-trigger").click();
    await expect(page.getByTestId("check-dependencies-demo-note")).toBeVisible();
    await expect(page.getByTestId("dependency-add-button")).toHaveCount(0);

    // The escalation picker itself STAYS — escalationPolicyUid rides the
    // allowlisted PATCH body — so the option list opening at all is the
    // positive control here. What is missing from it is the silent shortcut.
    await page.getByTestId("escalation-policy-select").click();
    await expect(page.getByTestId("escalation-option-inherit")).toBeVisible({
      timeout: 20000,
    });
    await expect(page.getByTestId("escalation-option-silent")).toHaveCount(0);
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("escalation-option-inherit")).toHaveCount(0);

    const renamed = `${name}-renamed`;
    await page.getByTestId("check-name-input").fill(renamed);
    await page.getByTestId("check-submit-button").click();

    // Back on the detail page, showing the new name — and no refusal toast.
    await page.waitForURL(/\/checks\/[0-9a-f-]{36}$/, { timeout: 30000 });
    await expect(
      page.getByTestId("check-detail-header").getByText(renamed),
    ).toBeVisible({ timeout: 20000 });
    await expect(page.getByText(/read-only live demo/i)).toHaveCount(0);

    // And deleting a check you created still works.
    await page.goto(checkUrl);
    await page.waitForLoadState("networkidle");
    await page
      .getByTestId("check-detail-header")
      .getByRole("button", { name: "Delete", exact: true })
      .click();
    await page
      .getByRole("alertdialog")
      .getByRole("button", { name: "Delete", exact: true })
      .click();
    await page.waitForURL(/\/checks(\?.*)?$/, { timeout: 20000 });
    await expect(page.getByText(renamed)).toHaveCount(0);

    expect(
      policyPosts,
      "a demo session must never be able to create an escalation policy",
    ).toHaveLength(0);
  });

  test("an ordinary session is still offered the silent-escalation shortcut", async ({
    page,
  }) => {
    // The negative control for the test above. Withholding the shortcut is a
    // demo boundary, not a feature removal: a customer whose org owns no
    // zero-step policy must still be able to create one from the picker, which
    // is exactly the branch the demo may not take.
    await page.goto("orgs/test/login");
    await page.waitForLoadState("networkidle");
    await page.getByTestId("login-title").waitFor({ state: "visible", timeout: 20000 });
    await page.getByTestId("login-email").fill("test@test.com");
    await page.getByTestId("login-password").fill("test");
    await page.getByTestId("login-submit").click();
    // Not a /orgs/test regex: the login page's own URL matches that too, so a
    // failed sign-in would sail past and fail later on something unrelated.
    await page.waitForURL((url) => !url.pathname.includes("login"), {
      timeout: 20000,
    });

    await page.goto("orgs/test/checks/new");
    await page.waitForLoadState("networkidle");
    await page.getByTestId("check-name-input").waitFor({
      state: "visible",
      timeout: 20000,
    });

    await page.getByTestId("escalation-policy-select").click();
    await expect(page.getByTestId("escalation-option-silent")).toBeVisible({
      timeout: 20000,
    });
    await page.keyboard.press("Escape");
  });

  test("the banner is on every page and cannot be dismissed", async ({ page }) => {
    await page.goto("orgs/test/login?demo=1");
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });

    const url = new URL(page.url());
    const org = url.pathname.split("/orgs/")[1]?.split("/")[0];
    expect(org).toBeTruthy();

    for (const path of ["checks", "incidents", "status-pages"]) {
      await page.goto(`orgs/${org}/${path}`);
      await page.waitForLoadState("networkidle");
      await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });
    }

    // No dismiss affordance anywhere on the banner.
    const banner = page.getByTestId("demo-banner");
    await expect(banner.getByRole("button", { name: /dismiss|close/i })).toHaveCount(0);
  });

  test("the settings pages offer a read-only note instead of a New button", async ({
    page,
  }) => {
    await page.goto("orgs/test/login?demo=1");
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });

    const url = new URL(page.url());
    const org = url.pathname.split("/orgs/")[1]?.split("/")[0];

    await page.goto(`orgs/${org}/integrations`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("integrations-demo-note")).toBeVisible({
      timeout: 20000,
    });

    await page.goto(`orgs/${org}/status-pages`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("status-pages-demo-note")).toBeVisible({
      timeout: 20000,
    });
  });

  test("a demo session can create a check and is told it will expire", async ({
    page,
    request,
  }) => {
    const demo = await demoConfig(request);
    const org = demo?.orgSlug as string;
    expect(org).toBeTruthy();

    await page.goto("orgs/test/login?demo=1");

    // Entering the demo from ANOTHER org's login page must land in the DEMO
    // org, not in the org whose login page this happened to be. Waited for
    // explicitly rather than read off page.url() once the banner shows: the
    // app settles on the demo org but transiently passes back through the
    // originating org on the way, so a single read can catch the intermediate
    // URL and then drive the rest of the test against an org this session is
    // not a member of. Asserting the destination is also the point — it is the
    // regression this test exists to catch.
    await page.waitForURL(new RegExp(`/orgs/${org}(/|$)`), { timeout: 20000 });
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });

    await page.goto(`orgs/${org}/checks/new`);
    await page.waitForLoadState("networkidle");

    // Creating a check is the ONE thing the demo exists to let a visitor do,
    // so this is the test that would catch a guard that closed too far.
    const slug = `e2e-demo-${Date.now()}`;

    const nameField = page.getByTestId("check-name-input");
    await nameField.waitFor({ state: "visible", timeout: 20000 });
    await nameField.fill(slug);

    const urlField = page.getByTestId("check-url-input");
    await urlField.waitFor({ state: "visible", timeout: 20000 });
    await urlField.fill(`${API_BASE}/api/v1/fake?period=86400`);

    await page.getByTestId("check-submit-button").click();

    // Landing on the detail page proves the create succeeded; the note is the
    // conversion hook.
    await page.waitForURL(/\/checks\/[0-9a-f-]{36}/, { timeout: 30000 });
    await expect(page.getByTestId("demo-check-note")).toBeVisible({ timeout: 20000 });
  });

  test("a write outside the allowlist is refused with DEMO_READ_ONLY", async ({
    page,
    request,
  }) => {
    // Straight at the API, because that is where the guarantee lives: the UI
    // merely declines to offer these buttons.
    const demo = await demoConfig(request);
    expect(demo?.enabled).toBe(true);

    const login = await request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: demo?.orgSlug, email: demo?.email, password: demo?.password },
    });
    expect(login.ok()).toBeTruthy();

    const { accessToken } = await login.json();
    expect(accessToken).toBeTruthy();

    const refused = await request.post(
      `${API_BASE}/api/v1/orgs/${demo?.orgSlug}/status-pages`,
      {
        headers: { Authorization: `Bearer ${accessToken}` },
        data: { name: "Nope", slug: "nope" },
      },
    );

    expect(refused.status()).toBe(403);
    expect((await refused.json()).code).toBe("DEMO_READ_ONLY");

    // Positive control: reading is fine, so the credential itself is good.
    const read = await request.get(`${API_BASE}/api/v1/orgs/${demo?.orgSlug}/checks`, {
      headers: { Authorization: `Bearer ${accessToken}` },
    });
    expect(read.ok()).toBeTruthy();

    await page.close();
  });

  test("a seeded catalogue check cannot be deleted", async ({ page, request }) => {
    const demo = await demoConfig(request);

    const login = await request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: demo?.orgSlug, email: demo?.email, password: demo?.password },
    });
    const { accessToken } = await login.json();

    const list = await request.get(`${API_BASE}/api/v1/orgs/${demo?.orgSlug}/checks`, {
      headers: { Authorization: `Bearer ${accessToken}` },
    });
    const { data } = await list.json();

    const seeded = (data as { uid: string; createdBy?: string | null }[]).find(
      (check) => !check.createdBy,
    );
    expect(seeded, "the demo org should carry a seeded, un-owned catalogue").toBeTruthy();

    const refused = await request.delete(
      `${API_BASE}/api/v1/orgs/${demo?.orgSlug}/checks/${seeded?.uid}`,
      { headers: { Authorization: `Bearer ${accessToken}` } },
    );

    expect(refused.status()).toBe(403);
    expect((await refused.json()).code).toBe("DEMO_READ_ONLY");

    await page.close();
  });

  // The edit ROUTE, not just the detail page, must refuse to render a form
  // the server would reject on save (spec
  // 2026-09-07-02-untranslated-strings-and-demo-refusal-message §B.4). A demo
  // visitor can reach /checks/<uid>/edit directly — the checks-list row menu,
  // a breadcrumb, a bookmarked URL — without ever seeing the detail page's
  // explanation.
  test("a seeded catalogue check's edit route shows the read-only note and a Clone button, not a form", async ({
    page,
    request,
  }) => {
    const demo = await demoConfig(request);

    const login = await request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: demo?.orgSlug, email: demo?.email, password: demo?.password },
    });
    const { accessToken } = await login.json();

    const list = await request.get(`${API_BASE}/api/v1/orgs/${demo?.orgSlug}/checks`, {
      headers: { Authorization: `Bearer ${accessToken}` },
    });
    const { data } = await list.json();

    const seeded = (data as { uid: string; createdBy?: string | null }[]).find(
      (check) => !check.createdBy,
    );
    expect(seeded, "the demo org should carry a seeded, un-owned catalogue").toBeTruthy();

    await page.goto("orgs/test/login?demo=1");
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });
    const url = new URL(page.url());
    const org = url.pathname.split("/orgs/")[1]?.split("/")[0];

    await page.goto(`orgs/${org}/checks/${seeded?.uid}/edit`);
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("demo-check-note")).toBeVisible({ timeout: 20000 });
    await expect(page.getByTestId("check-edit-clone-button")).toBeVisible();
    // The form itself must never have rendered — not just be hidden behind
    // the note — since the whole point is a visitor never fills out fields
    // the server will refuse on submit.
    await expect(page.getByTestId("check-name-input")).toHaveCount(0);
  });

  // Hide what cannot be done on the status page detail too (spec §B.5):
  // POST .../sections and .../resources are outside the demo write
  // allowlist, so offering the add affordances would only end in a refusal
  // toast — mirrors status-pages.index.tsx and integrations.index.tsx.
  test("the status page detail hides the add-section affordance for a demo session", async ({
    page,
  }) => {
    await page.goto("orgs/test/login?demo=1");
    await expect(page.getByTestId("demo-banner")).toBeVisible({ timeout: 20000 });
    const url = new URL(page.url());
    const org = url.pathname.split("/orgs/")[1]?.split("/")[0];

    await page.goto(`orgs/${org}/status-pages/demo`);
    await page.waitForLoadState("networkidle");

    // AddSectionDialog renders once in the header and again in the empty
    // state when the page has no sections yet — both instances render the
    // note for a demo session, so assert on the first rather than a single
    // match.
    await expect(
      page.getByTestId("status-page-add-section-demo-note").first(),
    ).toBeVisible({ timeout: 20000 });
    await expect(page.getByLabel("Add Section")).toHaveCount(0);
  });

  test("the demo account's password cannot be reset", async ({ request }) => {
    // The unauthenticated path the write guard cannot see. A bare request
    // without a valid token is refused anyway; what matters here is that
    // requesting a reset for the demo address does not silently rotate the
    // shared credential — the login below is the proof.
    const demo = await demoConfig(request);

    await request.post(`${API_BASE}/api/v1/auth/request-password-reset`, {
      data: { org: demo?.orgSlug, email: demo?.email },
    });

    const login = await request.post(`${API_BASE}/api/v1/auth/login`, {
      data: { org: demo?.orgSlug, email: demo?.email, password: demo?.password },
    });

    expect(login.ok()).toBeTruthy();
  });
});
