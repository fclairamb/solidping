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
//
// It echoes the secret REVERSED rather than comparing it to a literal. That is
// not squeamishness: a literal would put the fixture secret into the `script`
// key — which is public config — and defeat the "the value appears nowhere in
// the response" assertion below. Reversing still determines the exact value, so
// the test proves the script read it rather than merely received something.
const SCRIPT =
  `var p = secrets.PASSWORD || ""; var rev = p.split("").reverse().join(""); ` +
  `return { status: (p && env.BASE_URL) ? "up" : "down", output: { rev: rev, base: env.BASE_URL } };`;

const SECRET_REVERSED = [...SECRET_VALUE].reverse().join("");

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
    //
    // `configPrivateKeys` alone is NOT proof: a form that submits `secrets: {}`
    // wipes the values while the key list stays exactly the same. Only a run
    // that happened AFTER this save can tell the two apart.
    //
    // Freshness is decided by result UID, not by a timestamp: a check runs once
    // immediately on creation, and this test is fast enough that a wall-clock
    // window wide enough for skew also lets that creation-time result through —
    // which is precisely the result that would hide the wipe.
    type ResultRow = { uid: string; output?: Record<string, unknown> };

    const listResults = async (): Promise<ResultRow[]> => {
      const resp = await page.request.get(
        `${API_BASE}/api/v1/orgs/test/results?checkUid=${uid}&with=output&limit=20`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      return (await resp.json()).data ?? [];
    };

    const alreadySeen = new Set((await listResults()).map((row) => row.uid));

    // Speed the schedule up to the type's floor so the next run lands promptly.
    // Period-only PATCH: the config is untouched by this request.
    const patched = await page.request.patch(
      `${API_BASE}/api/v1/orgs/test/checks/${uid}`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: { period: "30s" },
      },
    );
    expect(patched.status()).toBe(200);

    const freshOutput = async (): Promise<Record<string, unknown> | null> => {
      const rows = await listResults();
      const fresh = rows.find(
        (row) => !alreadySeen.has(row.uid) && row.output?.rev !== undefined,
      );
      return fresh?.output ?? null;
    };

    await expect
      .poll(freshOutput, {
        timeout: 150_000,
        intervals: [2000],
        message:
          "no new result from the js check — the script never ran again, or it could not read its config",
      })
      .not.toBeNull();

    const output = (await freshOutput())!;

    expect(
      output.rev,
      "after an untouched save the script no longer reads secrets.PASSWORD",
    ).toBe(SECRET_REVERSED);
    expect(
      output.base,
      "after an untouched save the script no longer reads env.BASE_URL",
    ).toBe(BASE_URL_VALUE);

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
