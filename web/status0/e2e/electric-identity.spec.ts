import { test, expect, type Page } from "@playwright/test";
import { API_BASE as BASE, STATUS_BASE } from "./fixtures";

/**
 * Spec 2026-09-24-04: the public status page wears dash0's electric identity
 * (tokens, and a gradient on its two buttons) WITHOUT taking anything away
 * from customer theming.
 *
 * The page payload is mocked, so every status the assertions need (an up, a
 * degraded, a down and a maintenance resource) is on screen on any server,
 * and the custom stylesheet is whatever the test says it is.
 *
 * The "renders identically" test only asserts values the operator wrote, so
 * it holds on the pre-electric build as well as on this one: it was run
 * against both. Source-level guards (token sync with dash0, no
 * higher-specificity token declaration) live in src/theme-tokens.test.ts.
 */

const ORG = "e2e-electric";
const SLUG = "electric";

// 1x1 transparent GIF — a self-contained "uploaded logo".
const PIXEL =
  "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7";

function resource(n: number, name: string, status: string, inMaintenance = false) {
  return {
    uid: `00000000-0000-0000-0000-00000000000${n}`,
    checkUid: `00000000-0000-0000-0000-0000000000a${n}`,
    position: n,
    check: { name, type: "http", status, inMaintenance },
  };
}

function payload(overrides: Record<string, unknown> = {}) {
  return {
    uid: "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee",
    name: "Electric Page",
    slug: SLUG,
    visibility: "public",
    isDefault: false,
    enabled: true,
    showAvailability: false,
    showResponseTime: false,
    historyDays: 0,
    historyPeriod: "24h",
    availabilityThresholds: { thresholdUp: 99.9, thresholdDegraded: 99 },
    overallStatus: "degraded",
    sections: [
      {
        uid: "ffffffff-ffff-ffff-ffff-ffffffffffff",
        name: "Core",
        slug: "core",
        position: 0,
        resources: [
          resource(1, "API", "up"),
          resource(2, "Search", "degraded"),
          resource(3, "Mail", "down"),
          resource(4, "Billing", "up", true),
        ],
      },
    ],
    ...overrides,
  };
}

async function openPage(page: Page, overrides: Record<string, unknown> = {}) {
  await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(payload(overrides)),
    }),
  );
  await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
  await expect(page.getByTestId("resource-row")).toHaveCount(4, {
    timeout: 15000,
  });
}

/** A probe: a name, a CSS selector, and the computed property to read. */
type Probe = [name: string, selector: string, property: string];

/**
 * Reads computed styles in ONE browser round-trip. Colors come back as
 * `rgb(r, g, b)` whatever notation the browser computes (Chrome keeps
 * `oklch(…)` for oklch tokens): a 1x1 canvas does the conversion, so the
 * assertions can be written against the operator's hex values.
 */
async function readStyles(page: Page, probes: Probe[]) {
  return page.evaluate((list) => {
    const canvas = document.createElement("canvas");
    canvas.width = 1;
    canvas.height = 1;
    const ctx = canvas.getContext("2d", { willReadFrequently: true })!;
    const toRgb = (color: string) => {
      ctx.clearRect(0, 0, 1, 1);
      ctx.fillStyle = "#000";
      ctx.fillStyle = color;
      ctx.fillRect(0, 0, 1, 1);
      const [r, g, b] = ctx.getImageData(0, 0, 1, 1).data;
      return `rgb(${r}, ${g}, ${b})`;
    };
    const out: Record<string, string> = {};
    for (const [name, selector, property] of list) {
      const el = document.querySelector(selector);
      if (!el) {
        out[name] = `<missing ${selector}>`;
        continue;
      }
      const value = getComputedStyle(el).getPropertyValue(property).trim();
      out[name] = /color$/i.test(property) ? toRgb(value) : value;
    }
    return out;
  }, probes);
}

// The section card: the Card that wraps the resource rows.
const CARD = ".bg-card:has([data-testid='resource-row'])";
const DOT = (n: number) =>
  `[data-testid='resource-row']:nth-child(${n}) [data-testid='resource-status-dot']`;

/** Every probe a documented (pre-electric) variable drives. */
function documentedProbes(theme: "light" | "dark"): Probe[] {
  const probes: Probe[] = [
    ["--background", "body", "background-color"],
    ["--foreground", "body", "color"],
    ["--border", "header", "border-bottom-color"],
    ["--card", CARD, "background-color"],
    ["--card-foreground", CARD, "color"],
    ["--radius", CARD, "border-top-left-radius"],
    ["--muted-foreground", ".sp-footer", "color"],
    ["--brand", ".sp-powered-by", "color"],
    ["--status-ok", DOT(1), "background-color"],
    ["--status-warning", DOT(2), "background-color"],
    ["--status-error", DOT(3), "background-color"],
  ];
  // Dark cards draw their outline in white/10 (elevation from light, not
  // --border) — true before this spec too, so only light asserts it.
  if (theme === "light") probes.push(["card --border", CARD, "border-top-color"]);
  return probes;
}

test.describe("Public status page — electric identity", () => {
  test.describe("default palette", () => {
    for (const theme of ["light", "dark"] as const) {
      test(`${theme}: page tokens and the gradient subscribe button`, async ({
        page,
      }) => {
        await page.emulateMedia({ colorScheme: theme });
        await openPage(page);

        const styles = await readStyles(page, [
          ["background", "body", "background-color"],
          ["foreground", "body", "color"],
          ["buttonLabel", "[data-testid='subscribe-submit']", "color"],
          ["buttonColor", "[data-testid='subscribe-submit']", "background-color"],
        ]);
        // Spec 01 §1 hex column: #f5f9fc / #09121f light, #060a13 dark.
        expect(styles.background).toBe(
          theme === "light" ? "rgb(245, 249, 252)" : "rgb(6, 10, 19)",
        );
        if (theme === "light") expect(styles.foreground).toBe("rgb(9, 18, 31)");
        // White label on the gradient, over the bg-primary fallback
        // (#1e64ef light, #57a8ff dark).
        expect(styles.buttonLabel).toBe("rgb(255, 255, 255)");
        expect(styles.buttonColor).toBe(
          theme === "light" ? "rgb(30, 100, 239)" : "rgb(87, 168, 255)",
        );

        const button = page.getByTestId("subscribe-submit");
        const image = await button.evaluate(
          (el) => getComputedStyle(el).backgroundImage,
        );
        expect(image).toContain("linear-gradient");
        expect(image).toContain("oklch(0.53 0.22 262)");
        // The inner top highlight stacks with the --primary-tinted shadow.
        expect(
          await button.evaluate((el) => getComputedStyle(el).boxShadow),
        ).toContain("inset");
      });
    }

    test("the buttons are the only gradient-filled elements", async ({ page }) => {
      await openPage(page);

      // Everything painting --primary-gradient, identified by its middle stop.
      const painted = await page.evaluate(() =>
        [...document.querySelectorAll<HTMLElement>("body *")]
          .filter((el) =>
            getComputedStyle(el).backgroundImage.includes("oklch(0.53 0.22 262)"),
          )
          .map((el) => el.dataset.testid ?? el.tagName),
      );
      expect(painted).toEqual(["subscribe-submit"]);

      // And no decorative SolidPing glow anywhere on the customer's page.
      const radial = await page.evaluate(() =>
        [...document.querySelectorAll("*")].some((el) =>
          getComputedStyle(el).backgroundImage.includes("radial-gradient"),
        ),
      );
      expect(radial).toBe(false);
    });

    test("the unlock button uses the same gradient", async ({ page }) => {
      await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}`, (route) =>
        route.fulfill({
          status: 401,
          contentType: "application/json",
          body: JSON.stringify({
            title: "This status page is password protected",
            code: "STATUS_PAGE_LOCKED",
          }),
        }),
      );
      await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);

      const button = page.getByTestId("status-page-unlock-submit");
      await expect(button).toBeVisible({ timeout: 15000 });
      expect(
        await button.evaluate((el) => getComputedStyle(el).backgroundImage),
      ).toContain("oklch(0.53 0.22 262)");
      const styles = await readStyles(page, [
        ["label", "[data-testid='status-page-unlock-submit']", "color"],
      ]);
      expect(styles.label).toBe("rgb(255, 255, 255)");
    });

    test("the TV shell sits on the dark page background", async ({ page }) => {
      await page.route(`**/api/v1/status-pages/${ORG}/**`, (route) =>
        route.fulfill({
          status: 404,
          contentType: "application/json",
          body: JSON.stringify({
            title: "Status page not found",
            code: "STATUS_PAGE_NOT_FOUND",
          }),
        }),
      );
      // Light visitor on purpose: a TV is dark whatever the browser prefers.
      await page.emulateMedia({ colorScheme: "light" });
      await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}/tv`);
      await expect(page.getByTestId("tv-not-found")).toBeVisible({
        timeout: 15000,
      });

      const styles = await readStyles(page, [
        ["background", "[data-testid='tv-shell']", "background-color"],
        ["foreground", "[data-testid='tv-shell']", "color"],
      ]);
      // Dark --background #060a13 and --foreground #edf2f9.
      expect(styles).toEqual({
        background: "rgb(6, 10, 19)",
        foreground: "rgb(237, 242, 249)",
      });
    });
  });

  test.describe("customer theming", () => {
    // Every variable documented BEFORE this spec, overridden in both themes.
    const CUSTOMER_CSS = `
      :root {
        --brand: #ff5500;
        --brand-foreground: #fff7f0;
        --background: #fdfaf7;
        --foreground: #201a14;
        --card: #fffdf9;
        --card-foreground: #2a221b;
        --border: #d9cfc4;
        --muted: #f3ede6;
        --muted-foreground: #7a6a5a;
        --status-ok: #118844;
        --status-warning: #cc8800;
        --status-error: #cc2233;
        --radius: 3px;
      }
      .dark {
        --brand: #ff7733;
        --brand-foreground: #1a0a00;
        --background: #12100e;
        --foreground: #f2ede8;
        --card: #1c1917;
        --card-foreground: #efe8e1;
        --border: #3a332c;
        --muted: #26211c;
        --muted-foreground: #a89886;
        --status-ok: #22aa55;
        --status-warning: #ddaa11;
        --status-error: #ee4455;
        --radius: 3px;
      }
    `;

    // What each probe must resolve to: the operator's value, i.e. what the
    // page rendered before the electric identity, since these probes read
    // documented variables only.
    const EXPECTED = {
      light: {
        "--background": "rgb(253, 250, 247)",
        "--foreground": "rgb(32, 26, 20)",
        "--border": "rgb(217, 207, 196)",
        "--card": "rgb(255, 253, 249)",
        "--card-foreground": "rgb(42, 34, 27)",
        // rounded-xl = calc(var(--radius) + 4px)
        "--radius": "7px",
        "--muted-foreground": "rgb(122, 106, 90)",
        "--brand": "rgb(255, 85, 0)",
        "--status-ok": "rgb(17, 136, 68)",
        "--status-warning": "rgb(204, 136, 0)",
        "--status-error": "rgb(204, 34, 51)",
        "card --border": "rgb(217, 207, 196)",
      },
      dark: {
        "--background": "rgb(18, 16, 14)",
        "--foreground": "rgb(242, 237, 232)",
        "--border": "rgb(58, 51, 44)",
        "--card": "rgb(28, 25, 23)",
        "--card-foreground": "rgb(239, 232, 225)",
        "--radius": "7px",
        "--muted-foreground": "rgb(168, 152, 134)",
        "--brand": "rgb(255, 119, 51)",
        "--status-ok": "rgb(34, 170, 85)",
        "--status-warning": "rgb(221, 170, 17)",
        "--status-error": "rgb(238, 68, 85)",
      },
    };

    for (const theme of ["light", "dark"] as const) {
      test(`${theme}: a stylesheet overriding every documented variable renders as the operator wrote it`, async ({
        page,
      }) => {
        await page.emulateMedia({ colorScheme: theme });

        // Positive control: without the stylesheet the page is NOT on the
        // operator's colors, so the match below is the stylesheet's doing.
        await openPage(page);
        const baseline = await readStyles(page, documentedProbes(theme));
        expect(baseline["--background"]).not.toBe(EXPECTED[theme]["--background"]);
        expect(baseline["--status-ok"]).not.toBe(EXPECTED[theme]["--status-ok"]);

        await page.unrouteAll({ behavior: "ignoreErrors" });
        await openPage(page, { customCss: CUSTOMER_CSS });
        const themed = await readStyles(page, documentedProbes(theme));
        expect(themed).toEqual(EXPECTED[theme]);
      });
    }

    for (const theme of ["light", "dark"] as const) {
      test(`${theme}: the new button variables re-theme the buttons`, async ({
        page,
      }) => {
        await page.emulateMedia({ colorScheme: theme });
        const selector = theme === "light" ? ":root" : ".dark";
        await openPage(page, {
          // The page-level maintenance rollup is drawn in --primary.
          overallStatus: "maintenance",
          customCss: `${selector} {
            --primary: #ff5500;
            --primary-foreground: #111111;
            --primary-gradient: linear-gradient(#ff5500, #ff5500);
            --gradient-foreground: #111111;
          }`,
        });

        const button = page.getByTestId("subscribe-submit");
        const image = await button.evaluate(
          (el) => getComputedStyle(el).backgroundImage,
        );
        expect(image).toContain("rgb(255, 85, 0)");
        expect(image).not.toContain("oklch(0.53 0.22 262)");

        const styles = await readStyles(page, [
          ["fill", "[data-testid='subscribe-submit']", "background-color"],
          ["label", "[data-testid='subscribe-submit']", "color"],
          // --primary also drives the maintenance state.
          ["maintenance", "[data-testid='overall-status-badge']", "color"],
        ]);
        expect(styles).toEqual({
          fill: "rgb(255, 85, 0)",
          label: "rgb(17, 17, 17)",
          maintenance: "rgb(255, 85, 0)",
        });
      });
    }

    test("the unlock button follows the same variables", async ({ page }) => {
      // A locked page has no payload, so no stylesheet reaches it: this only
      // proves the unlock button reads the same variables, by setting them on
      // the document the way a stylesheet would.
      await page.route(`**/api/v1/status-pages/${ORG}/${SLUG}`, (route) =>
        route.fulfill({
          status: 401,
          contentType: "application/json",
          body: JSON.stringify({
            title: "This status page is password protected",
            code: "STATUS_PAGE_LOCKED",
          }),
        }),
      );
      await page.goto(`${BASE}${STATUS_BASE}/${ORG}/${SLUG}`);
      await expect(page.getByTestId("status-page-unlock-submit")).toBeVisible({
        timeout: 15000,
      });
      await page.addStyleTag({
        content: `:root {
          --primary: #ff5500;
          --primary-gradient: linear-gradient(#ff5500, #ff5500);
          --gradient-foreground: #111111;
        }`,
      });

      // Polled: the button's `transition` animates a color change made after
      // it painted (a real operator stylesheet is there from first paint).
      await expect
        .poll(() =>
          readStyles(page, [
            ["fill", "[data-testid='status-page-unlock-submit']", "background-color"],
            ["label", "[data-testid='status-page-unlock-submit']", "color"],
          ]),
        )
        .toEqual({ fill: "rgb(255, 85, 0)", label: "rgb(17, 17, 17)" });
      expect(
        await page
          .getByTestId("status-page-unlock-submit")
          .evaluate((el) => getComputedStyle(el).backgroundImage),
      ).toContain("rgb(255, 85, 0)");
    });

    test("a white-label page shows no SolidPing mark and no credit", async ({
      page,
    }) => {
      // Positive control: the stock page carries both.
      await openPage(page);
      await expect(page.locator("img[alt='SolidPing']")).toHaveCount(1);
      await expect(page.locator(".sp-powered-by")).toHaveCount(1);

      await page.unrouteAll({ behavior: "ignoreErrors" });
      await openPage(page, { hideBranding: true, logoUrl: PIXEL });
      await expect(page.getByTestId("status-page-logo")).toBeVisible();
      await expect(page.locator("img[alt='SolidPing']")).toHaveCount(0);
      await expect(page.locator(".sp-powered-by")).toHaveCount(0);
      await expect(page.locator("body")).not.toContainText("SolidPing");
    });
  });
});
