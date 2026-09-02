import { test, expect, API_BASE, type Page } from "./fixtures";
import { expandSection } from "./section-helpers";

// The shared `ipVersion` option (spec 2026-08-09-02): a check can be pinned to
// IPv4 or IPv6, so a dual-stack target is actually verified over the family you
// care about instead of an IPv4-only probe quietly reporting "up" while every
// IPv6 user is down.
//
// What only an end-to-end test reaches:
//   - the selector is gated on server-declared capability metadata
//     (`supportsIpVersion`), so it shows on tcp/http/icmp and NOT on dns;
//   - picking a family through the real form actually stores `ipVersion`, and
//     clearing back to Auto removes the key rather than storing a literal;
//   - the API refuses an invalid value and refuses the option on a type that
//     cannot honor it, so the write-time gate is real and not just form state.

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

// createTcpCheck creates a tcp check pinned to a family, over the API, and
// returns its uid. Port 9 (discard) is closed on the runner, which is fine: the
// assertions are about which address family was selected, not about reaching a
// service.
async function createTcpCheck(
  page: Page,
  token: string,
  opts: { name: string; host: string; ipVersion: string },
): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      name: opts.name,
      type: "tcp",
      period: "10s",
      config: { host: opts.host, port: 9, ipVersion: opts.ipVersion },
    },
  });
  expect(resp.status()).toBe(201);
  return (await resp.json()).uid;
}

// waitForResult polls until the check has actually executed once.
async function waitForResult(
  page: Page,
  token: string,
  uid: string,
): Promise<{ output: Record<string, string | undefined> }> {
  for (let attempt = 0; attempt < 40; attempt++) {
    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/checks/${uid}?with=last_result`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    const body = await resp.json();
    if (body.lastResult?.output) {
      return body.lastResult;
    }
    await page.waitForTimeout(1000);
  }
  throw new Error(`check ${uid} produced no result in time`);
}

async function getCheck(page: Page, token: string, uid: string) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  expect(resp.status()).toBe(200);
  return await resp.json();
}

test.describe("IP version selector", () => {
  test("appears only on types that can be pinned to a family", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    for (const type of ["tcp", "http", "icmp"]) {
      await page.goto(`orgs/test/checks/new?checkType=${type}`);
      await page.waitForLoadState("networkidle");
      await expandSection(page, "section-advanced-trigger");
      await expect(page.getByTestId("check-ip-version-section")).toBeVisible();
    }

    // dns is deliberately scoped out: there, "IP version" could mean which
    // record types to assert on OR which transport reaches the nameserver —
    // two different features. The form reads that from the API's
    // `supportsIpVersion`, never from a hard-coded list here.
    await page.goto("orgs/test/checks/new?checkType=dns");
    await page.waitForLoadState("networkidle");
    await expandSection(page, "section-advanced-trigger");
    await expect(page.getByTestId("check-ip-version-section")).toHaveCount(0);

    // Same for a type that dials by name through a client library and exposes
    // no address-family seam.
    await page.goto("orgs/test/checks/new?checkType=postgresql");
    await page.waitForLoadState("networkidle");
    await expandSection(page, "section-advanced-trigger");
    await expect(page.getByTestId("check-ip-version-section")).toHaveCount(0);
  });

  test("pins a family, persists it, and clears back to auto", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    await page.goto("orgs/test/checks/new?checkType=tcp");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    await page.getByTestId("check-name-input").fill(`E2E IPv6 ${Date.now()}`);
    await page.getByTestId("check-host-input").fill("example.com");
    await page.getByTestId("check-port-input").fill("443");

    await expandSection(page, "section-advanced-trigger");
    await page.getByTestId("check-ip-version-select").click();
    await page.getByRole("option", { name: "IPv6 only", exact: true }).click();

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });
    await page.waitForLoadState("networkidle");
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    // The selection actually reached the stored config, on the canonical key.
    const created = await getCheck(page, token, uid);
    expect(created.config.ipVersion).toBe("ipv6");

    // The resolved family is surfaced on the detail page whenever a result
    // carries it (a freshly created check may not have run yet, so this only
    // asserts the row's shape when it is present).
    const ipVersionRow = page.getByTestId("last-result-ip-version");
    if ((await ipVersionRow.count()) > 0) {
      await expect(ipVersionRow).toContainText("ipv6");
    }

    // Editing reopens on the stored value — the Advanced section auto-expands
    // because a pinned family counts as a customized advanced option.
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expandSection(page, "section-advanced-trigger");
    await expect(page.getByTestId("check-ip-version-select")).toContainText(
      "IPv6",
    );

    // Clearing back to Auto removes the key rather than storing a literal
    // "auto", so an unpinned check's config is byte-identical to one that never
    // touched the option.
    await page.getByTestId("check-ip-version-select").click();
    await page
      .getByRole("option", { name: "Auto (default)", exact: true })
      .click();
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });
    // The networkidle wait is load-bearing here, not decoration. This submit
    // starts on /checks/<uid>/edit, which ALREADY matches the regex above, so
    // waitForURL resolves instantly and getCheck below would race the PATCH
    // and read back the pre-clear "ipv6". The create half above waits for the
    // same reason; omitting it here made this test fail ~2 runs in 3.
    await page.waitForLoadState("networkidle");

    const cleared = await getCheck(page, token, uid);
    expect(cleared.config.ipVersion).toBeUndefined();

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("the API rejects an invalid value and an unsupported type", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // A typo must fail loudly rather than silently monitoring the wrong family.
    const bad = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: `E2E bad ipVersion ${Date.now()}`,
        type: "tcp",
        config: { host: "example.com", port: 443, ipVersion: "v6" },
      },
    });
    // 422 with a field-scoped VALIDATION_ERROR, the shape the form binds to.
    expect(bad.status()).toBe(422);
    const badBody = await bad.json();
    expect(badBody.code).toBe("VALIDATION_ERROR");
    expect(badBody.fields[0].name).toBe("ipVersion");
    expect(badBody.fields[0].message).toContain("auto, ipv4 or ipv6");

    // And the option on a type that cannot honor it is refused, not ignored.
    const dns = await page.request.post(`${API_BASE}/api/v1/orgs/test/checks`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        name: `E2E dns ipVersion ${Date.now()}`,
        type: "dns",
        config: { host: "example.com", ipVersion: "ipv6" },
      },
    });
    expect(dns.status()).toBe(422);
    const dnsBody = await dns.json();
    expect(dnsBody.fields[0].name).toBe("ipVersion");
    expect(dnsBody.fields[0].message).toContain("cannot be pinned");
  });

  test("the worker honors the pinned family and reports it back", async ({
    authenticatedPage,
  }) => {
    // This is the only place the whole chain is exercised: the stored config
    // key reaches the worker, the worker puts the family on the execution
    // context, and the checker's address pick honors it. A form test cannot see
    // any of that.
    const page = authenticatedPage;
    const token = await getAuthToken(page);
    const stamp = Date.now();

    // 127.0.0.1 has no AAAA by construction, so an ipv6 pin must fail with the
    // cataloged message naming the host and the family — never a generic dial
    // error that would send the user hunting through their own service.
    const pinned = await createTcpCheck(page, token, {
      name: `E2E wire v6 ${stamp}`,
      host: "127.0.0.1",
      ipVersion: "ipv6",
    });

    // localhost resolves to at least IPv4 everywhere, so the ipv4 pin gets past
    // selection and reports the family it used. This is the positive control:
    // without it, the assertion above could pass on a check that never ran.
    const control = await createTcpCheck(page, token, {
      name: `E2E wire v4 ${stamp}`,
      host: "localhost",
      ipVersion: "ipv4",
    });

    const pinnedResult = await waitForResult(page, token, pinned);
    expect(pinnedResult.output.error).toContain(
      "no address of the requested IP version",
    );
    expect(pinnedResult.output.error).toContain("127.0.0.1");
    expect(pinnedResult.output.error).toContain("IPv6");

    const controlResult = await waitForResult(page, token, control);
    expect(controlResult.output.ip_version).toBe("ipv4");
    expect(controlResult.output.error ?? "").not.toContain(
      "no address of the requested IP version",
    );

    // The resolved family is visible on the check detail page, next to the raw
    // output rather than buried in it.
    await page.goto(`orgs/test/checks/${control}`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("last-result-ip-version")).toContainText(
      "ipv4",
    );

    for (const uid of [pinned, control]) {
      await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
    }
  });
});
