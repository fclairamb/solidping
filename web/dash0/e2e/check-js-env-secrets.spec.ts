import { test, expect, API_BASE, type Page } from "./fixtures";

// Coverage for spec 2026-09-11-05: a `js` check carries two maps — a public
// `env` and an encrypted `secrets` — and the form must treat them differently.
//
// `env` round-trips on GET, so it is shown and re-sent. `secrets` never comes
// back, so the editor starts empty on every edit and must NOT be serialized
// until the operator touches it: omitting the key is what makes the server's
// preserve-absent-secrets merge keep the stored value. Sending it anyway is the
// "save wipes the secret" bug class (2026-05-18-07, 2026-08-28-12).
//
// The last assertion is the one that cannot be faked by the API shape: after a
// save that touched neither map, the check RUNS and its script reports that it
// still read both values.

const SECRET_VALUE = "js-e2e-secret";
const BASE_URL_VALUE = "https://acme.dev";

// One line on purpose: the script goes into a CodeMirror editor, and a
// single-line insert avoids auto-indent/auto-close mangling multi-line text.
// It compares BOTH values and reports only whether they matched — the secret
// itself never goes into the result output.
const SCRIPT =
  `return { status: (secrets.PASSWORD === "${SECRET_VALUE}" && env.BASE_URL === "${BASE_URL_VALUE}") ? "up" : "down", ` +
  `output: { match: (secrets.PASSWORD === "${SECRET_VALUE}" && env.BASE_URL === "${BASE_URL_VALUE}") ? "yes" : "no", base: env.BASE_URL } };`;

async function getAuthToken(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: "test", email: "test@test.com", password: "test" },
  });
  return (await resp.json()).accessToken;
}

async function getCheck(page: Page, token: string, uid: string) {
  const resp = await page.request.get(
    `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
    { headers: { Authorization: `Bearer ${token}` } },
  );
  expect(resp.status()).toBe(200);
  return await resp.json();
}

test.describe("JS check — public env vs encrypted secrets", () => {
  // The final assertion waits for the scheduler to actually run the check.
  test.setTimeout(180_000);

  test("env is shown and secrets are not, and an untouched save keeps both", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // ── Create, through the real form ──
    await page.goto("orgs/test/checks/new?checkType=js");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("check-name-input")).toBeVisible();

    const checkName = `E2E JS Secrets ${Date.now()}`;
    await page.getByTestId("check-name-input").fill(checkName);

    // CodeMirror is a contenteditable; fill() inserts the text in one go.
    await page.getByTestId("check-script").locator(".cm-content").fill(SCRIPT);

    await page.getByTestId("js-env-add").click();
    await page.getByTestId("js-env-key-0").fill("BASE_URL");
    await page.getByTestId("js-env-value-0").fill(BASE_URL_VALUE);

    await page.getByTestId("js-secret-add").click();
    await page.getByTestId("js-secret-key-0").fill("PASSWORD");
    await page.getByTestId("js-secret-value-0").fill(SECRET_VALUE);

    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f]{8}-/, { timeout: 15000 });
    await page.waitForLoadState("networkidle");
    const uid = page.url().match(/\/checks\/([0-9a-f-]{36})/)![1];

    // The split at rest: env is public, secrets is not — and the secret value
    // is nowhere in the response, in ANY storage mode (CI runs the plaintext
    // fallback with no master key).
    const created = await getCheck(page, token, uid);
    expect(created.config.env).toEqual({ BASE_URL: BASE_URL_VALUE });
    expect(created.config).not.toHaveProperty("secrets");
    expect(created.configPrivateKeys).toContain("secrets");
    expect(JSON.stringify(created)).not.toContain(SECRET_VALUE);

    // ── Reload the edit page: env shown, secrets not ──
    await page.goto(`orgs/test/checks/${uid}/edit`);
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("js-env-key-0")).toBeVisible();

    await expect(page.getByTestId("js-env-key-0")).toHaveValue("BASE_URL");
    await expect(page.getByTestId("js-env-value-0")).toHaveValue(BASE_URL_VALUE);
    // The secrets editor comes back EMPTY, with a placeholder saying the value
    // is stored — not echoed back into an input.
    await expect(page.getByTestId("js-secret-key-0")).toHaveCount(0);
    await expect(page.getByTestId("js-secret-encrypted")).toBeVisible();

    // ── Save without touching either map ──
    await page.getByTestId("check-submit-button").click();
    await page.waitForURL(/\/checks\/[0-9a-f-]{36}$/, { timeout: 15000 });
    await page.waitForLoadState("networkidle");

    const saved = await getCheck(page, token, uid);
    expect(saved.config.env).toEqual({ BASE_URL: BASE_URL_VALUE });
    expect(
      saved.configPrivateKeys,
      "an untouched save destroyed the stored secrets",
    ).toContain("secrets");

    // ── And the check still WORKS: run it and read what the script saw ──
    // Speed the schedule up to the type's floor so a result lands promptly.
    // Period-only PATCH: the config is untouched by this request.
    const patched = await page.request.patch(
      `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: { period: "30s" },
      },
    );
    expect(patched.status()).toBe(200);

    await expect
      .poll(
        async () => {
          const resp = await page.request.get(
            `${API_BASE}/api/v1/orgs/test/results?checkUid=${uid}&with=output&limit=10`,
            { headers: { Authorization: `Bearer ${token}` } },
          );
          const body = await resp.json();
          const rows: { output?: Record<string, unknown> }[] = body.data ?? [];
          const withOutput = rows.find((row) => row.output?.match !== undefined);
          return withOutput?.output ?? null;
        },
        {
          timeout: 150_000,
          intervals: [2000],
          message:
            "no result from the js check — the script never ran, or it could not read its config",
        },
      )
      .not.toBeNull();

    const resp = await page.request.get(
      `${API_BASE}/api/v1/orgs/test/results?checkUid=${uid}&with=output&limit=10`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    const rows: { output?: Record<string, unknown> }[] =
      (await resp.json()).data ?? [];
    const output = rows.find((row) => row.output?.match !== undefined)!.output!;

    expect(
      output.match,
      "after an untouched save the script no longer reads both secrets.PASSWORD and env.BASE_URL",
    ).toBe("yes");
    expect(output.base).toBe(BASE_URL_VALUE);

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
