import {
  test as base,
  expect,
  type Locator,
  type Page,
} from "@playwright/test";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { smoothstep, type CueFile, type FocusCue, type Rect } from "./crop-window";

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
 *
 * ## Three layers this file provides
 *
 * 1. **Bootstrap** — login (including the forced password rotation a fresh
 *    default-mode database now imposes), org provisioning, demo data.
 * 2. **Human input** — a painted cursor, eased mouse travel, per-character
 *    typing. Headless Chromium paints no pointer, so without this a viewer
 *    watches buttons click themselves.
 * 3. **Cue points** — `focus()` records *what mattered when*; the browser is
 *    never zoomed. `postprocess.ts` turns the cue list into the camera move.
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
const BOOTSTRAP_EMAIL = process.env.SHOWCASE_EMAIL ?? "admin@solidping.io";
const SEEDED_PASSWORD = process.env.SHOWCASE_PASSWORD ?? "solidpass";

/**
 * The password the pipeline rotates the bootstrap account onto when the server
 * demands a rotation.
 *
 * It **must differ from the seeded one**: since spec 2026-08-23-04 a fresh
 * database seeds `admin@solidping.io` with `MustChangePassword = true`
 * (`server/internal/jobs/jobtypes/job_startup.go`), and
 * `POST /auth/change-password` rejects reusing the current password with
 * `400 VALIDATION_ERROR` ("new password must be different from the current
 * one", `server/internal/handlers/auth/service.go`). Rotating `solidpass` back
 * onto itself is therefore not an option — the account genuinely ends up on
 * this value, and stays on it.
 */
export const ROTATED_PASSWORD =
  process.env.SHOWCASE_ROTATED_PASSWORD ?? "showcase-rotated-pass";

/**
 * The password that actually works right now. Starts at the seeded value and
 * moves to {@link ROTATED_PASSWORD} once a rotation happens, so `uiLogin()`
 * types the right thing without the spec having to thread it through.
 */
let effectivePassword = SEEDED_PASSWORD;

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

/** Where cue lists are written, one per recording. Also git-ignored. */
export const CUES_DIR = path.join(showcaseDir, "output", "cues");

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

interface LoginBody {
  accessToken: string;
  user?: { mustChangePassword?: boolean };
}

/**
 * Logs in through the API against the bootstrap org, satisfying a forced
 * password rotation if the server imposes one.
 *
 * Since spec 2026-08-23-04 a fresh default-mode database seeds
 * `admin@solidping.io` with `MustChangePassword = true`. The login itself
 * succeeds — by design — but the session it hands back reaches only
 * `POST /auth/change-password`, `GET /auth/me` and `POST /auth/logout`; every
 * other endpoint answers `403 PASSWORD_CHANGE_REQUIRED`
 * (`server/internal/middleware/auth.go`). The very next thing this pipeline
 * does is `POST /api/v1/orgs`, so without this the whole run dies one call in.
 *
 * The gate reads the user row rather than a token claim, and `ChangePassword`
 * deliberately spares the caller's own refresh grant, so the token obtained
 * before the rotation keeps working afterwards — no re-login needed.
 */
export async function apiLogin(page: Page): Promise<string> {
  let password = SEEDED_PASSWORD;
  let resp = await postLogin(page, password);

  // A rerun against a side-car database this pipeline already rotated will
  // reject the seeded password. Try the rotated one before giving up.
  if (resp.status() === 401 && ROTATED_PASSWORD !== SEEDED_PASSWORD) {
    const retry = await postLogin(page, ROTATED_PASSWORD);
    if (retry.ok()) {
      password = ROTATED_PASSWORD;
      resp = retry;
      console.log(
        "showcase: the seeded password was rejected; signed in with " +
          "SHOWCASE_ROTATED_PASSWORD (this database has been rotated before).",
      );
    }
  }

  if (!resp.ok()) {
    throw new Error(
      `Showcase bootstrap login failed (${resp.status()}) as ${BOOTSTRAP_EMAIL} ` +
        `in org "${BOOTSTRAP_ORG}" against ${API_BASE}. Is a SolidPing server ` +
        `running there? Override with SHOWCASE_BOOTSTRAP_ORG / SHOWCASE_EMAIL / ` +
        `SHOWCASE_PASSWORD.`,
    );
  }

  effectivePassword = password;
  const body = (await resp.json()) as LoginBody;
  const token = body.accessToken;

  if (await rotationRequired(page, token, body)) {
    await rotatePassword(page, token, password);
  }

  return token;
}

function postLogin(page: Page, password: string) {
  return page.request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { org: BOOTSTRAP_ORG, email: BOOTSTRAP_EMAIL, password },
  });
}

/**
 * Whether the account is confined to the rotation screen.
 *
 * Believes the login response, but also probes `GET /api/v1/auth/me` — which is
 * on the rotation allowlist — so an older server that only surfaces the flag
 * there is handled too.
 */
async function rotationRequired(
  page: Page,
  token: string,
  login: LoginBody,
): Promise<boolean> {
  if (login.user?.mustChangePassword === true) return true;
  const resp = await page.request.get(`${API_BASE}/api/v1/auth/me`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!resp.ok()) return false;
  const body = (await resp.json()) as { user?: { mustChangePassword?: boolean } };
  return body.user?.mustChangePassword === true;
}

async function rotatePassword(
  page: Page,
  token: string,
  currentPassword: string,
): Promise<void> {
  if (ROTATED_PASSWORD === currentPassword) {
    throw new Error(
      "The showcase account must rotate its password, but " +
        "SHOWCASE_ROTATED_PASSWORD is identical to the current one. " +
        "POST /auth/change-password rejects that with 400 VALIDATION_ERROR " +
        "(\"new password must be different from the current one\"). Set " +
        "SHOWCASE_ROTATED_PASSWORD to something else.",
    );
  }

  const resp = await page.request.post(
    `${API_BASE}/api/v1/auth/change-password`,
    {
      headers: { Authorization: `Bearer ${token}` },
      data: { currentPassword, newPassword: ROTATED_PASSWORD },
    },
  );
  if (!resp.ok()) {
    throw new Error(
      `The showcase account is flagged for a forced password rotation, but ` +
        `POST /api/v1/auth/change-password answered ${resp.status()}: ` +
        `${await resp.text()}. Password policy is a minimum of 8 characters and ` +
        `a value different from the current one.`,
    );
  }

  effectivePassword = ROTATED_PASSWORD;
  console.log(
    `showcase: ${BOOTSTRAP_EMAIL} was flagged for a forced password rotation; ` +
      `it has been rotated to SHOWCASE_ROTATED_PASSWORD ("${ROTATED_PASSWORD}") ` +
      `and STAYS on that password. Reusing the seeded one is refused by the ` +
      `server, so this is not reversible in-run.`,
  );
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

// ---------------------------------------------------------------------------
// Cursor overlay
// ---------------------------------------------------------------------------

/**
 * Whether the synthetic cursor is painted. On by default; `SHOWCASE_CURSOR=0`
 * turns it off.
 *
 * The SMS opt-in capture deliberately never installs it: those stills are
 * evidence submitted to a carrier reviewer, and a cursor we drew ourselves is
 * exactly the kind of embellishment that must not appear on them.
 */
const CURSOR_ENABLED = process.env.SHOWCASE_CURSOR !== "0";

/**
 * Paints a synthetic pointer into the page.
 *
 * Headless Chromium does not composite the OS cursor into its screencast, so
 * without this every click looks like the UI operating itself — and a zoom has
 * nothing to follow. The overlay is `pointer-events: none` and lives outside
 * the app's DOM subtree, so it cannot alter what is being demonstrated.
 *
 * Must be called before the first navigation: `addInitScript` re-runs on every
 * document, but not retroactively on the current one.
 */
export async function installCursor(page: Page): Promise<void> {
  if (!CURSOR_ENABLED) return;
  await page.addInitScript(() => {
    const ID = "__showcase_cursor__";

    const install = (): void => {
      if (document.getElementById(ID)) return;

      const root = document.createElement("div");
      root.id = ID;
      root.style.cssText =
        "position:fixed;left:0;top:0;width:0;height:0;z-index:2147483647;" +
        "pointer-events:none;";

      const arrow = document.createElement("div");
      arrow.style.cssText =
        "position:absolute;left:0;top:0;width:24px;height:24px;opacity:0;" +
        "transition:opacity 120ms linear;will-change:transform;" +
        "filter:drop-shadow(0 1px 2px rgba(0,0,0,0.35));";
      arrow.innerHTML =
        '<svg width="24" height="24" viewBox="0 0 24 24" ' +
        'xmlns="http://www.w3.org/2000/svg">' +
        '<path d="M6 2.5 L6 19.4 L10.3 15.3 L12.9 21.6 L15.7 20.4 ' +
        'L13.1 14.2 L18.9 13.8 Z" fill="#111827" stroke="#ffffff" ' +
        'stroke-width="1.4" stroke-linejoin="round"/></svg>';
      root.appendChild(arrow);
      document.documentElement.appendChild(root);

      let hidden = false;
      let seen = false;

      const paint = (x: number, y: number): void => {
        arrow.style.transform = `translate(${x}px, ${y}px)`;
        arrow.style.opacity = hidden ? "0" : "1";
      };

      addEventListener(
        "mousemove",
        (event) => {
          seen = true;
          paint(event.clientX, event.clientY);
        },
        true,
      );

      addEventListener(
        "mousedown",
        (event) => {
          if (hidden) return;
          const ripple = document.createElement("div");
          ripple.style.cssText =
            "position:absolute;left:0;top:0;width:14px;height:14px;" +
            "margin:-7px 0 0 -7px;border-radius:50%;" +
            "border:2px solid rgba(37,99,235,0.9);" +
            "background:rgba(37,99,235,0.18);" +
            `transform:translate(${event.clientX}px, ${event.clientY}px) scale(0.4);`;
          root.appendChild(ripple);
          ripple
            .animate(
              [
                {
                  transform: `translate(${event.clientX}px, ${event.clientY}px) scale(0.4)`,
                  opacity: 1,
                },
                {
                  transform: `translate(${event.clientX}px, ${event.clientY}px) scale(3)`,
                  opacity: 0,
                },
              ],
              { duration: 450, easing: "ease-out" },
            )
            .addEventListener("finish", () => ripple.remove());
        },
        true,
      );

      // Screenshots are published as-is, so the pointer steps out of frame for
      // them: a still is a picture of the product, not of the recording rig.
      (
        window as unknown as {
          __showcaseCursor?: { hide(): void; show(): void };
        }
      ).__showcaseCursor = {
        hide: () => {
          hidden = true;
          arrow.style.opacity = "0";
        },
        show: () => {
          hidden = false;
          if (seen) arrow.style.opacity = "1";
        },
      };
    };

    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", install);
    } else {
      install();
    }
  });
}

async function setCursorVisible(page: Page, visible: boolean): Promise<void> {
  if (!CURSOR_ENABLED) return;
  await page
    .evaluate((show) => {
      const api = (
        window as unknown as {
          __showcaseCursor?: { hide(): void; show(): void };
        }
      ).__showcaseCursor;
      if (show) api?.show();
      else api?.hide();
    }, visible)
    .catch(() => undefined);
}

// ---------------------------------------------------------------------------
// Human input
// ---------------------------------------------------------------------------

/** Default travel time for a cursor move, in ms. */
const TRAVEL_MS = Number(process.env.SHOWCASE_TRAVEL_MS ?? 420);

/** Where the synthetic pointer currently is, in CSS px. */
let pointer = { x: 640, y: 720 };

/**
 * Deterministic jitter, so two runs of the same script type at the same rhythm.
 * `Math.random()` would make the cue timeline drift between runs for no gain.
 */
let seed = 0x5f3759df;
function nextRandom(): number {
  seed = (seed * 1664525 + 1013904223) >>> 0;
  return seed / 0x1_0000_0000;
}

async function centreOf(target: Locator): Promise<{ x: number; y: number }> {
  const box = await target.boundingBox();
  if (!box) {
    throw new Error(
      "showcase: cannot move the cursor to an element with no bounding box — " +
        "it is detached or not visible.",
    );
  }
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}

/**
 * Moves the (synthetic) pointer to an element in eased steps.
 *
 * Playwright's own `mouse.move(..., { steps })` emits every intermediate event
 * in one burst, which the screencast cannot see. This walks the path in real
 * time instead, with the same smoothstep the post-production zoom uses, so the
 * two read as one motion.
 */
export async function moveTo(
  page: Page,
  target: Locator,
  options: { durationMs?: number } = {},
): Promise<void> {
  const to = await centreOf(target);
  const duration = options.durationMs ?? TRAVEL_MS;
  const steps = Math.max(2, Math.round(duration / 16));
  const from = pointer;

  for (let i = 1; i <= steps; i++) {
    const eased = smoothstep(i / steps);
    await page.mouse.move(
      from.x + (to.x - from.x) * eased,
      from.y + (to.y - from.y) * eased,
    );
    if (i < steps) await page.waitForTimeout(duration / steps);
  }
  pointer = to;
}

/** Travels to an element, then clicks it. Every on-camera click goes through this. */
export async function clickOn(
  page: Page,
  target: Locator,
  options: { durationMs?: number } = {},
): Promise<void> {
  await moveTo(page, target, options);
  await target.click();
}

/**
 * Types text one character at a time with a human-ish cadence.
 *
 * `fill()` — what this replaces — sets the whole value in a single frame, which
 * on camera reads as a paste, not as somebody using the product.
 */
export async function typeHuman(
  page: Page,
  target: Locator,
  text: string,
  options: { minDelayMs?: number; maxDelayMs?: number } = {},
): Promise<void> {
  const min = options.minDelayMs ?? 40;
  const max = options.maxDelayMs ?? 70;
  await target.focus();
  for (const char of text) {
    await target.pressSequentially(char);
    await page.waitForTimeout(min + nextRandom() * (max - min));
  }
}

// ---------------------------------------------------------------------------
// Cue points (the camera move, recorded but not applied)
// ---------------------------------------------------------------------------

let cueAnchorMs: number | null = null;
const recordedCues: FocusCue[] = [];

/**
 * Starts the cue timeline. `t = 0` is *now*.
 *
 * Called from {@link uiLogin} the instant the login screen paints, because that
 * is precisely what ends the recording's opening frozen frame — the one
 * `postprocess.ts` already locates with `freezedetect`. That shared landmark is
 * how cue times survive the trim: Playwright never tells us when the video's
 * first frame was captured.
 */
export function beginCueTimeline(): void {
  cueAnchorMs = Date.now();
  recordedCues.length = 0;
}

function unionRect(a: Rect | null, b: Rect): Rect {
  if (!a) return b;
  const x = Math.min(a.x, b.x);
  const y = Math.min(a.y, b.y);
  return {
    x,
    y,
    width: Math.max(a.x + a.width, b.x + b.width) - x,
    height: Math.max(a.y + a.height, b.y + b.height) - y,
  };
}

/**
 * Records "from here on, this is what matters".
 *
 * Pass a locator (or several, to frame their union) to push in on it, or `null`
 * to return to the full frame. The browser is not touched: the recording is
 * bit-for-bit what it would have been without the call. `postprocess.ts` reads
 * the cue list back and moves the camera in post, which is why the motion is
 * smooth regardless of how jerkily Playwright drove the UI.
 */
export async function focus(
  page: Page,
  target: Locator | Locator[] | null,
  options: {
    zoom?: number;
    transitionMs?: number;
    holdMs?: number;
    label?: string;
  } = {},
): Promise<void> {
  if (cueAnchorMs == null) return;

  let rect: Rect | null = null;
  if (target) {
    for (const locator of Array.isArray(target) ? target : [target]) {
      const box = await locator.boundingBox().catch(() => null);
      if (box) rect = unionRect(rect, box);
    }
  }

  recordedCues.push({
    t: (Date.now() - cueAnchorMs) / 1000,
    rect,
    zoom: options.zoom,
    transitionMs: options.transitionMs,
    holdMs: options.holdMs,
    label: options.label,
  });

  if (options.holdMs) await page.waitForTimeout(options.holdMs);
}

/**
 * Writes the cue list next to the recording it belongs to.
 *
 * Keyed off the video's own directory name (Playwright names every recording
 * `video.webm` inside a per-test folder), so `postprocess.ts` pairs cues with
 * the right take even when several showcase specs each produce a video.
 *
 * Call it from a `finally` — a failed take's cues are still worth having when
 * debugging why the framing was off.
 */
export async function writeCues(page: Page, fallbackName: string): Promise<void> {
  if (cueAnchorMs == null) return;

  let name = fallbackName;
  const video = page.video();
  if (video) {
    const videoPath = await video.path().catch(() => null);
    if (videoPath) name = path.basename(path.dirname(videoPath));
  }

  const viewport = page.viewportSize() ?? { width: 1280, height: 800 };
  const file: CueFile = { version: 1, viewport, cues: recordedCues };

  await mkdir(CUES_DIR, { recursive: true });
  await writeFile(
    path.join(CUES_DIR, `${name}.json`),
    `${JSON.stringify(file, null, 2)}\n`,
    "utf8",
  );
  console.log(
    `showcase: wrote ${recordedCues.length} cue points to ` +
      `${path.relative(showcaseDir, path.join(CUES_DIR, `${name}.json`))}`,
  );
}

// ---------------------------------------------------------------------------
// Capture helpers
// ---------------------------------------------------------------------------

/**
 * Writes a named still frame into the predictable stills directory.
 *
 * The synthetic cursor is hidden for the shot: the stills are published as
 * product screenshots, where a pointer we invented would be noise.
 */
export async function still(page: Page, name: string): Promise<void> {
  await mkdir(STILLS_DIR, { recursive: true });
  await setCursorVisible(page, false);
  try {
    await page.screenshot({ path: path.join(STILLS_DIR, `${name}.png`) });
  } finally {
    await setCursorVisible(page, true);
  }
}

/** A scripted pause, so the viewer has time to read what just happened. */
export async function beat(page: Page, ms = 1200): Promise<void> {
  await page.waitForTimeout(ms);
}

/**
 * Logs in through the real form, into the showcase org.
 *
 * Types {@link effectivePassword}, not the seeded constant: when the server
 * forced a rotation during {@link apiLogin}, the seeded one no longer works.
 */
export async function uiLogin(page: Page): Promise<void> {
  await page.goto(`orgs/${SHOWCASE_ORG}/login`);
  await page.waitForLoadState("networkidle");
  await page
    .getByTestId("login-title")
    .waitFor({ state: "visible", timeout: 15000 });

  // The blank opening frame of the recording ends here — this is the cue
  // timeline's t = 0, and the landmark postprocess.ts realigns against.
  beginCueTimeline();

  await page.getByTestId("login-email").fill(BOOTSTRAP_EMAIL);
  await page.getByTestId("login-password").fill(effectivePassword);
  await page.getByTestId("login-submit").click();
  await page.waitForURL((url) => !url.pathname.includes("login"), {
    timeout: 15000,
  });
  await page.waitForLoadState("networkidle");
}

export { base as test, expect, type Locator, type Page };
