import {
  test,
  expect,
  mockSloCoverage,
  API_BASE,
  getAuthToken,
  createHeartbeatCheck,
} from "./fixtures";

/**
 * Spec 2026-09-02-04: a heartbeat check writes two kinds of raw row that used
 * to be indistinguishable — the BEAT the caller sent, and the SCHEDULER
 * EVALUATION a checks worker writes every period. Both read "Heartbeat
 * received" with status Up, so opening the evaluation row that lands seconds
 * after a ping showed no Caller card and read as "my ping was not recorded".
 *
 * Two tests, deliberately different in kind:
 *
 *  1. the real flow, with no mocks at all — the only way to prove the backend
 *     actually stamps the marker and that dash0 asks for `output` on this
 *     table for passive check types;
 *  2. the rendering of an evaluation row against fixtures, which is the only
 *     way to control the beat/evaluation pair precisely enough to assert the
 *     "N before this evaluation" lead and the region-dropping "Open the
 *     signal" link.
 */

const beatUid = "bbbbbbbb-0000-7000-8000-000000000001";
const evaluationUid = "bbbbbbbb-0000-7000-8000-000000000002";

// The beat landed 12 s before the evaluation looked at it.
const beatAt = "2026-09-02T12:36:38Z";
const evaluatedAt = "2026-09-02T12:36:50Z";

const detailFixtures: Record<string, Record<string, unknown>> = {
  [beatUid]: {
    uid: beatUid,
    status: "up",
    periodStart: beatAt,
    periodType: "raw",
    // A beat: caller metadata, no region, and crucially no `evaluation` key.
    output: {
      message: "Heartbeat received",
      userAgent: "curl/8.4.0",
      remoteAddr: "203.0.113.9",
      httpMethod: "POST",
    },
  },
  [evaluationUid]: {
    uid: evaluationUid,
    status: "up",
    periodStart: evaluatedAt,
    periodType: "raw",
    region: "eu",
    output: {
      message: "Heartbeat on time",
      evaluation: true,
      lastSignalAt: beatAt,
      lastSignalResultUid: beatUid,
    },
  },
};

test.describe("Heartbeat evaluation rows are distinguishable from beats", () => {
  test("real flow: the evaluation row is badged and explains itself, the beat is not", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const token = await getAuthToken(page);

    // A one-hour period means exactly ONE scheduler evaluation can run inside
    // this test: the one that fires ~1 s after creation. Anything shorter and
    // the table would fill with evaluations while the test runs, and "exactly
    // one of each" would be a race.
    const check = await createHeartbeatCheck(
      page,
      token,
      `E2E Evaluation Rows ${Date.now()}`,
      undefined,
      "01:00:00",
    );

    const readResults = async (): Promise<
      { uid: string; output?: Record<string, unknown> }[]
    > => {
      const resp = await page.request.get(
        `${API_BASE}/api/v1/orgs/test/results?checkUid=${check.uid}&with=output&limit=50`,
        { headers: { Authorization: `Bearer ${token}` } },
      );
      const body = await resp.json();
      return body.data ?? [];
    };

    // 1. Wait for the creation-time evaluation row (status down — nothing has
    //    pinged yet). It lands within ~1 s of check creation.
    let evaluationRowUid = "";
    await expect
      .poll(
        async () => {
          const rows = await readResults();
          const found = rows.find((row) => row.output?.evaluation === true);
          evaluationRowUid = found?.uid ?? "";
          return Boolean(found);
        },
        { timeout: 30_000, message: "the creation-time evaluation row never appeared" },
      )
      .toBe(true);

    // 2. Only THEN send the beat, so the two rows can't be confused by order.
    const beat = await page.request.get(
      `${API_BASE}/api/v1/heartbeat/test/${check.uid}?token=${check.hbToken}`,
    );
    expect(beat.status()).toBe(200);

    let beatRowUid = "";
    await expect
      .poll(
        async () => {
          const rows = await readResults();
          const found = rows.find(
            (row) => typeof row.output?.remoteAddr === "string",
          );
          beatRowUid = found?.uid ?? "";
          return Boolean(found);
        },
        { timeout: 30_000, message: "the beat row never appeared" },
      )
      .toBe(true);

    await mockSloCoverage(page);
    await page.goto(`orgs/test/checks/${check.uid}`);
    await page.waitForLoadState("networkidle");

    // Exactly one evaluation badge: the beat must NOT be badged.
    const badges = page.locator('[data-testid^="result-evaluation-badge-"]');
    await expect(badges).toHaveCount(1);
    await expect(
      page.getByTestId(`result-evaluation-badge-${evaluationRowUid}`),
    ).toBeVisible();

    // The evaluation row explains itself and offers no Caller card — the beat
    // it would point at does not exist yet at the moment it was written.
    await page.getByTestId(`result-row-${evaluationRowUid}`).click();
    await page.waitForURL(`**/results/${evaluationRowUid}**`, { timeout: 10000 });
    await expect(page.getByTestId("evaluation-card")).toBeVisible();
    await expect(page.getByTestId("evaluation-no-signal")).toBeVisible();
    await expect(page.getByTestId("caller-card")).toHaveCount(0);

    // The beat is the mirror image: a Caller card with the source IP, and no
    // evaluation card or badge anywhere.
    await page.goBack();
    await page.waitForURL(`**/checks/${check.uid}**`, { timeout: 10000 });
    await page.waitForLoadState("networkidle");
    await page.getByTestId(`result-row-${beatRowUid}`).click();
    await page.waitForURL(`**/results/${beatRowUid}**`, { timeout: 10000 });
    await expect(page.getByTestId("caller-card")).toBeVisible();
    await expect(page.getByTestId("caller-card")).toContainText("Source IP");
    await expect(page.getByTestId("evaluation-card")).toHaveCount(0);
    await expect(page.getByTestId("result-evaluation-badge")).toHaveCount(0);

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${check.uid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });

  test("mocked detail: the card names the beat and Open the signal drops the region", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await mockSloCoverage(page);

    await page.route("**/api/v1/orgs/*/checks/*/results/*", (route) => {
      const uid = new URL(route.request().url()).pathname.split("/").pop() ?? "";
      const fixture = detailFixtures[uid];
      if (!fixture) return route.continue();
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(fixture),
      });
    });

    // A real check has to exist for the route params to resolve; its type
    // (http here) is irrelevant — the card keys off the row's output, not the
    // check.
    const token = await getAuthToken(page);
    const created = await page.request.post(
      `${API_BASE}/api/v1/orgs/test/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: {
          name: `E2E Evaluation Detail ${Date.now()}`,
          type: "http",
          config: { url: "https://example.com/evaluation-detail" },
        },
      },
    );
    expect(created.status()).toBe(201);
    const checkUid = (await created.json()).uid as string;

    // Arrive with ?region=eu, exactly as a click from the region-filtered
    // Recent Results table would.
    await page.goto(
      `orgs/test/checks/${checkUid}/results/${evaluationUid}?region=eu`,
    );
    await page.waitForLoadState("networkidle");

    const card = page.getByTestId("evaluation-card");
    await expect(card).toBeVisible();
    await expect(card).toContainText("Heartbeat on time");
    await expect(page.getByTestId("evaluation-last-signal")).toBeVisible();
    await expect(card).toContainText("before this evaluation");
    await expect(page.getByTestId("result-evaluation-badge")).toBeVisible();

    // Every key the card owns — `message` included — is stripped from the raw
    // dump, so the Output card has nothing left to render and disappears.
    await expect(page.getByText('"evaluation": true')).toHaveCount(0);
    await expect(page.getByText('"lastSignalResultUid"')).toHaveCount(0);

    // Open the signal → the beat, WITHOUT the region param: the beat has no
    // region, so carrying ?region=eu would scope its prev/next neighbours to
    // a region it is not in.
    await page.getByTestId("evaluation-open-signal").click();
    await page.waitForURL(`**/results/${beatUid}*`, { timeout: 10000 });
    expect(page.url()).not.toContain("region=");

    await expect(page.getByTestId("caller-card")).toBeVisible();
    await expect(page.getByTestId("caller-card")).toContainText("203.0.113.9");
    await expect(page.getByTestId("evaluation-card")).toHaveCount(0);

    await page.request.delete(`${API_BASE}/api/v1/orgs/test/checks/${checkUid}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  });
});
