import { test, expect, type Page } from "./fixtures";

// Coverage for spec 2026-08-16-01: form controls must not be the same color as
// the page, and the segmented control's *selected* segment must be the raised
// (lighter) one.
//
// This asserts on computed background colors rather than class names, because
// the failure mode this guards against is a token inversion — dark --muted
// (0.22) is lighter than dark --card (0.18), so a track that reuses bg-muted in
// dark puts the pill *below* its own track. A class-name assertion cannot see
// that; a getComputedStyle assertion can.

/**
 * Rough lightness of a computed background-color string, comparable only
 * against other values produced by the same browser. Handles the oklch/oklab
 * serialization Chromium uses for oklch() authored colors as well as rgb().
 */
function lightnessOf(color: string): number {
  const oklch = /^okl(?:ch|ab)\(\s*([\d.]+%?)/.exec(color);
  if (oklch) {
    const raw = oklch[1];
    return raw.endsWith("%") ? parseFloat(raw) / 100 : parseFloat(raw);
  }
  const rgb = /^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/.exec(color);
  if (rgb) {
    const [r, g, b] = [rgb[1], rgb[2], rgb[3]].map(Number);
    return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
  }
  throw new Error(`unparseable color: ${color}`);
}

/** Computed background-color of a throwaway element carrying `className`. */
async function probe(page: Page, className: string): Promise<string> {
  return page.evaluate((cls) => {
    const el = document.createElement("div");
    el.className = cls;
    el.style.width = "10px";
    el.style.height = "10px";
    document.body.appendChild(el);
    const bg = getComputedStyle(el).backgroundColor;
    el.remove();
    return bg;
  }, className);
}

async function bgOf(page: Page, testId: string): Promise<string> {
  return page
    .getByTestId(testId)
    .first()
    .evaluate((el) => getComputedStyle(el).backgroundColor);
}

async function assertOrdering(page: Page, theme: "light" | "dark") {
  // Controls carry `transition-colors`, so right after a theme flip
  // getComputedStyle returns an interpolated (still half-white) color. Poll
  // until the input has settled rather than sampling mid-transition — freshly
  // created probe elements are not animating and would read the final value,
  // which would make this a false failure.
  await expect
    .poll(() => bgOf(page, "checks-search-input"), { timeout: 5000 })
    .toBe(await probe(page, "bg-control"));

  const background = await probe(page, "bg-background");
  const control = await probe(page, "bg-control");
  const card = await probe(page, "bg-card");
  const muted = await probe(page, "bg-muted");

  // The control surface must be visually distinct from the page it sits on.
  expect(control, `${theme}: --control must differ from --background`).not.toBe(
    background,
  );

  if (theme === "light") {
    // page < control <= card — controls are raised in light mode.
    expect(lightnessOf(control)).toBeGreaterThan(lightnessOf(background));
    expect(lightnessOf(control)).toBeLessThanOrEqual(lightnessOf(card));
  } else {
    // Dark deliberately diverges: controls are recessed, between the page and
    // the card. That divergence is the whole reason --control is a token.
    expect(lightnessOf(control)).toBeGreaterThan(lightnessOf(background));
    expect(lightnessOf(control)).toBeLessThan(lightnessOf(card));
  }

  // The real search input must actually be wired to the control token.
  expect(
    await bgOf(page, "checks-search-input"),
    `${theme}: the search input should use bg-control`,
  ).toBe(control);

  // Segmented control: the selected pill must be lighter than its track, in
  // both themes. In dark the track flips to bg-background precisely because
  // bg-muted would be lighter than the bg-card pill.
  await expect
    .poll(() => bgOf(page, "group-by-groups"), { timeout: 5000 })
    .toBe(card);
  const pill = await bgOf(page, "group-by-groups");
  const track = await page
    .getByTestId("group-by-groups")
    .evaluate((el) => getComputedStyle(el.parentElement!).backgroundColor);

  expect(pill, `${theme}: selected segment should use bg-card`).toBe(card);
  expect(
    lightnessOf(pill),
    `${theme}: selected pill must be lighter than its track (${pill} vs ${track})`,
  ).toBeGreaterThan(lightnessOf(track));

  if (theme === "dark") {
    // Positive control for the inversion this test exists to catch: if the
    // track had stayed bg-muted in dark, the pill would have been darker.
    expect(lightnessOf(muted)).toBeGreaterThan(lightnessOf(card));
    expect(track).toBe(background);
  } else {
    expect(track).toBe(muted);
  }
}

test.describe("control surface elevation", () => {
  test("page < control <= card, and the selected segment is the raised pill, in both themes", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto("orgs/test/checks");
    await page.waitForLoadState("networkidle");
    await expect(page.getByTestId("group-by-groups")).toBeVisible();

    const isDark = await page.evaluate(() =>
      document.documentElement.classList.contains("dark"),
    );
    if (isDark) {
      await page.getByTestId("theme-toggle").click();
      await expect
        .poll(() =>
          page.evaluate(() =>
            document.documentElement.classList.contains("dark"),
          ),
        )
        .toBe(false);
    }

    await assertOrdering(page, "light");

    await page.getByTestId("theme-toggle").click();
    await expect
      .poll(() =>
        page.evaluate(() =>
          document.documentElement.classList.contains("dark"),
        ),
      )
      .toBe(true);

    await assertOrdering(page, "dark");
  });
});
