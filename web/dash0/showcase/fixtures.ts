import { test as base, expect, type Page } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

/**
 * Showcase fixtures.
 *
 * Mirrors `web/dash0/e2e/fixtures.ts`: the server origin is derived from
 * `E2E_BASE_URL` so a side-car server on another port can be targeted without
 * disturbing a dev server on :4000, and never hardcoded.
 *
 * ## Why this does NOT record in the e2e fixture org
 *
 * These frames are published marketing assets. Recording in the
 * `SP_RUNMODE=test` org would put the test fixtures on camera — the seeded
 * "Notified Check → https://example.com" row and a `test@test.com /
 * Administrator` sidebar identity. So the pipeline instead **provisions its
 * own organization** through the public API (`POST /api/v1/orgs`) and records
 * there: a fresh org contains no fixture data at all, and its name is ours to
 * choose. The account's display name is set through `PATCH /api/v1/auth/me` so
 * the sidebar reads as a person, not a role label — and, because that endpoint
 * writes the **global** user record rather than an org-scoped profile
 * (`OrganizationMember` carries no name of its own), the original value is read
 * first and restored in the recording's `finally` path. A completed run leaves
 * the user record exactly as it found it.
 *
 * The recommended run mode is therefore the DEFAULT one (not `test`), whose
 * out-of-the-box account is `admin@solidping.io` / `solidpass` — an identity
 * that reads plausibly on camera. See `showcase/README.md`.
 */
export const API_BASE = process.env.E2E_BASE_URL
  ? new URL(process.env.E2E_BASE_URL).origin
  : "http://localhost:4000";

/**
 * Credentials used to bootstrap the recording. Defaults match a default-run-mode
 * server (`admin@solidping.io` / `solidpass`, org `default`). Override for a
 * server seeded differently.
 */
export const BOOTSTRAP_ORG = process.env.SHOWCASE_BOOTSTRAP_ORG ?? "default";
const BOOTSTRAP_EMAIL =
  process.env.SHOWCASE_EMAIL ?? "admin@solidping.io";
const BOOTSTRAP_PASSWORD = process.env.SHOWCASE_PASSWORD ?? "solidpass";

/**
 * The dedicated org the recording actually happens in — created on first run,
 * reused (and wiped clean) on later runs. Slug rules: 3-20 chars, lowercase
 * alphanumeric plus hyphens.
 */
export const SHOWCASE_ORG = process.env.SHOWCASE_ORG ?? "northwind";
const SHOWCASE_ORG_NAME = process.env.SHOWCASE_ORG_NAME ?? "Northwind Systems";

/** Display name shown in the dashboard sidebar during the recording. */
const SHOWCASE_USER_NAME = process.env.SHOWCASE_USER_NAME ?? "Alex Rivera";

const showcaseDir = path.dirname(fileURLToPath(import.meta.url));

/** Where named still frames are written. Git-ignored, like the rest of output/. */
export const STILLS_DIR = path.join(showcaseDir, "output", "stills");

/**
 * Realistic demo data staged before the recording so the UI on camera looks
 * like a real account.
 */
export const DEMO_CHECKS = [
  {
    name: "Marketing site",
    type: "http",
    config: { url: "https://www.solidping.io/" },
  },
  {
    name: "Docs site",
    type: "http",
    config: { url: "https://docs.solidping.io/" },
  },
  {
    name: "Checkout API",
    type: "http",
    config: { url: "https://api.solidping.io/health" },
  },
] as const;

/** The check created on camera during the recording. */
export const FEATURED_CHECK = {
  name: "Production API",
  url: "https://api.solidping.io/v1/health",
};

interface Json {
  [key: string]: unknown;
}

/** Logs in through the API against the bootstrap org. */
export async function apiLogin(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: {
      org: BOOTSTRAP_ORG,
      email: BOOTSTRAP_EMAIL,
      password: BOOTSTRAP_PASSWORD,
    },
  });
  if (!resp.ok()) {
    throw new Error(
      `Showcase bootstrap login failed (${resp.status()}) as ${BOOTSTRAP_EMAIL} ` +
        `in org "${BOOTSTRAP_ORG}" against ${API_BASE}. Is a SolidPing server ` +
        `running there? Override with SHOWCASE_BOOTSTRAP_ORG / SHOWCASE_EMAIL / ` +
        `SHOWCASE_PASSWORD.`,
    );
  }
  return (await resp.json()).accessToken;
}

/**
 * Ensures the dedicated showcase org exists and contains nothing but the demo
 * data this pipeline stages.
 *
 * - First run: `POST /api/v1/orgs` creates it (the caller becomes its admin).
 * - Later runs: the 409 is expected; every check already in the org is deleted
 *   so the recording never picks up leftovers from a previous run.
 *
 * Returns an access token scoped to the showcase org.
 */
export async function ensureCleanShowcaseOrg(
  page: Page,
  bootstrapToken: string,
): Promise<string> {
  const auth = { Authorization: `Bearer ${bootstrapToken}` };

  const createResp = await page.request.post(`${API_BASE}/api/v1/orgs`, {
    headers: auth,
    data: { name: SHOWCASE_ORG_NAME, slug: SHOWCASE_ORG },
  });
  if (createResp.status() !== 201 && createResp.status() !== 409) {
    throw new Error(
      `Could not provision the showcase org "${SHOWCASE_ORG}" ` +
        `(POST /api/v1/orgs → ${createResp.status()}): ${await createResp.text()}`,
    );
  }

  // Creating an org mints a session scoped to it; on a rerun (409) switch into
  // the existing one instead.
  let orgToken: string;
  if (createResp.status() === 201) {
    orgToken = (await createResp.json()).accessToken;
  } else {
    const switchResp = await page.request.post(
      `${API_BASE}/api/v1/auth/switch-org`,
      { headers: auth, data: { org: SHOWCASE_ORG } },
    );
    if (!switchResp.ok()) {
      throw new Error(
        `The showcase org "${SHOWCASE_ORG}" already exists but could not be ` +
          `switched into (${switchResp.status()}): ${await switchResp.text()}`,
      );
    }
    orgToken = (await switchResp.json()).accessToken;
  }

  // Wipe anything left over so only staged demo data ends up on camera.
  await deleteAllChecks(page, orgToken);

  return orgToken;
}

/**
 * Reads the authenticated user's current display name (empty string when unset).
 */
async function readProfileName(page: Page, token: string): Promise<string> {
  const resp = await page.request.get(`${API_BASE}/api/v1/auth/me`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!resp.ok()) {
    throw new Error(
      `Could not read the current profile (GET /api/v1/auth/me → ${resp.status()}). ` +
        "Refusing to overwrite a display name we cannot restore.",
    );
  }
  const body = (await resp.json()) as { user?: { name?: string | null } };
  return body.user?.name ?? "";
}

async function writeProfileName(
  page: Page,
  token: string,
  name: string,
): Promise<void> {
  await page.request.patch(`${API_BASE}/api/v1/auth/me`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { name },
  });
}

/**
 * Temporarily gives the recording account a human display name, so the sidebar
 * footer reads "Alex Rivera" instead of "Administrator".
 *
 * `PATCH /api/v1/auth/me` writes the GLOBAL user row (see
 * `server/internal/handlers/auth/service.go` → `UpdateProfile`), so this is a
 * borrowed change, never a permanent one: the caller MUST pass the returned
 * value to {@link restoreShowcaseIdentity} in a `finally` block. Without that,
 * recording against someone's dev server would silently rename their admin
 * account forever.
 */
export async function applyShowcaseIdentity(
  page: Page,
  token: string,
): Promise<string> {
  const previousName = await readProfileName(page, token);
  await writeProfileName(page, token, SHOWCASE_USER_NAME);
  return previousName;
}

/**
 * Puts the account's display name back exactly as it was. Best-effort: a
 * failure here must never mask whatever failure sent us into the `finally`.
 */
export async function restoreShowcaseIdentity(
  page: Page,
  token: string,
  previousName: string,
): Promise<void> {
  try {
    await writeProfileName(page, token, previousName);
  } catch {
    console.warn(
      `showcase: could not restore the account display name to ` +
        `"${previousName}" — it may still read "${SHOWCASE_USER_NAME}".`,
    );
  }
}

/**
 * Deletes every check in the showcase org.
 *
 * Entirely throw-safe, including the initial listing: this runs in the
 * recording's `finally` block, where a rejection (e.g. the server died
 * mid-run) would mask the original failure.
 */
export async function deleteAllChecks(
  page: Page,
  token: string,
): Promise<void> {
  const headers = { Authorization: `Bearer ${token}` };
  const listResp = await page.request
    .get(`${API_BASE}/api/v1/orgs/${SHOWCASE_ORG}/checks?limit=500`, { headers })
    .catch(() => null);
  if (!listResp?.ok()) return;
  const checks: Json[] = (await listResp.json().catch(() => ({}))).data ?? [];
  for (const check of checks) {
    await page.request
      .delete(
        `${API_BASE}/api/v1/orgs/${SHOWCASE_ORG}/checks/${String(check.uid)}`,
        { headers },
      )
      .catch(() => undefined);
  }
}

/** Seeds the staged demo checks into the showcase org. */
export async function seedDemoData(
  page: Page,
  token: string,
): Promise<string[]> {
  const uids: string[] = [];
  for (const check of DEMO_CHECKS) {
    const resp = await page.request.post(
      `${API_BASE}/api/v1/orgs/${SHOWCASE_ORG}/checks`,
      {
        headers: { Authorization: `Bearer ${token}` },
        data: check,
      },
    );
    if (resp.status() === 201) {
      uids.push((await resp.json()).uid);
    }
  }
  return uids;
}

/** Writes a named still frame into the predictable stills directory. */
export async function still(page: Page, name: string): Promise<void> {
  await mkdir(STILLS_DIR, { recursive: true });
  await page.screenshot({ path: path.join(STILLS_DIR, `${name}.png`) });
}

/** A scripted pause, so the viewer has time to read what just happened. */
export async function beat(page: Page, ms = 1200): Promise<void> {
  await page.waitForTimeout(ms);
}

/** Logs in through the real form, into the showcase org. */
export async function uiLogin(page: Page): Promise<void> {
  await page.goto(`orgs/${SHOWCASE_ORG}/login`);
  await page.waitForLoadState("networkidle");
  await page
    .getByTestId("login-title")
    .waitFor({ state: "visible", timeout: 15000 });
  await page.getByTestId("login-email").fill(BOOTSTRAP_EMAIL);
  await page.getByTestId("login-password").fill(BOOTSTRAP_PASSWORD);
  await page.getByTestId("login-submit").click();
  await page.waitForURL((url) => !url.pathname.includes("login"), {
    timeout: 15000,
  });
  await page.waitForLoadState("networkidle");
}

export { base as test, expect, type Page };
