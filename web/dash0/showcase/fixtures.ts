import { test as base, expect, type Page } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

/**
 * Showcase fixtures.
 *
 * Mirrors `web/dash0/e2e/fixtures.ts`: the server origin is derived from
 * `E2E_BASE_URL` so a side-car test server on another port can be targeted
 * without disturbing a dev server on :4000, and never hardcoded.
 */
export const API_BASE = process.env.E2E_BASE_URL
  ? new URL(process.env.E2E_BASE_URL).origin
  : "http://localhost:4000";

/** Credentials used for the recording. Defaults match `SP_RUNMODE=test`. */
export const SHOWCASE_ORG = process.env.SHOWCASE_ORG ?? "test";
const SHOWCASE_EMAIL = process.env.SHOWCASE_EMAIL ?? "test@test.com";
const SHOWCASE_PASSWORD = process.env.SHOWCASE_PASSWORD ?? "test";

const showcaseDir = path.dirname(fileURLToPath(import.meta.url));

/** Where named still frames are written. Git-ignored, like the rest of output/. */
export const STILLS_DIR = path.join(showcaseDir, "output", "stills");

/**
 * Realistic demo data staged before the recording so the UI on camera looks
 * like a real account rather than the e2e fixtures' throwaway rows.
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

export async function apiLogin(page: Page): Promise<string> {
  const resp = await page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: {
      org: SHOWCASE_ORG,
      email: SHOWCASE_EMAIL,
      password: SHOWCASE_PASSWORD,
    },
  });
  if (!resp.ok()) {
    throw new Error(
      `Showcase login failed (${resp.status()}) against ${API_BASE}. ` +
        `Is a SolidPing server running there? Override credentials with ` +
        `SHOWCASE_ORG / SHOWCASE_EMAIL / SHOWCASE_PASSWORD.`,
    );
  }
  return (await resp.json()).accessToken;
}

/** Seeds the staged demo checks; returns their uids for later cleanup. */
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

/** Removes everything the recording created, so the org is clean afterwards. */
export async function cleanupDemoData(
  page: Page,
  token: string,
  uids: string[],
): Promise<void> {
  for (const uid of uids) {
    await page.request
      .delete(`${API_BASE}/api/v1/orgs/${SHOWCASE_ORG}/checks/${uid}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      .catch(() => undefined);
  }
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

/** Logs in through the real form — the first thing the viewer sees working. */
export async function uiLogin(page: Page): Promise<void> {
  await page.goto(`orgs/${SHOWCASE_ORG}/login`);
  await page.waitForLoadState("networkidle");
  await page.getByTestId("login-title").waitFor({ state: "visible", timeout: 15000 });
  await page.getByTestId("login-email").fill(SHOWCASE_EMAIL);
  await page.getByTestId("login-password").fill(SHOWCASE_PASSWORD);
  await page.getByTestId("login-submit").click();
  await page.waitForURL((url) => !url.pathname.includes("login"), {
    timeout: 15000,
  });
  await page.waitForLoadState("networkidle");
}

export { base as test, expect, type Page };
