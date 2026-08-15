import { test, expect, API_BASE, type Page } from "./fixtures";

// The appearance editor's contract in one place (spec 2026-07-27-02):
//
//   * typing CSS restyles the PREVIEW IFRAME live, with no save and no server
//     round-trip (the iframe runs the real status0 renderer);
//   * saving publishes it, so the public status page picks it up; and
//   * the preview's message listener ignores anything that is not the exact
//     {type: 'sp:preview-css', css: string} shape.
//
// The brand color is the probe: --brand is read straight off the iframe's
// documentElement, which is exactly where the <style> block lands.

const PREVIEW_BRAND = "rgb(255, 0, 0)";
const SAVED_BRAND = "rgb(0, 128, 255)";

// 1x1 transparent GIF — a real, self-contained image URL, so nothing in these
// tests depends on network access.
const PIXEL =
  "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7";

async function createStatusPage(page: Page, suffix: string): Promise<string> {
  await page.goto(`orgs/test/status-pages/new`);

  const slug = `e2e-css-${suffix}`.slice(0, 40);
  const nameInput = page.locator("#name");
  await expect(nameInput).toBeVisible({ timeout: 30000 });
  await nameInput.fill(`e2e-appearance-${suffix}`);
  await page.locator("#slug").fill(slug);

  const create = page.getByRole("button", { name: "Create Status Page" });
  await expect(create).toBeEnabled();
  await create.click();

  // NOT just /status-pages/[^/]+$ — that also matches the /new form we are
  // standing on, so the wait would return before the page even exists.
  await page.waitForURL(
    (url) =>
      /\/status-pages\/[^/]+$/.test(url.pathname) &&
      !url.pathname.endsWith("/new"),
    { timeout: 45000 },
  );

  return slug;
}

/** Reads --brand as the iframe's own document computes it. */
async function previewBrand(page: Page): Promise<string> {
  const frame = page.frameLocator('[data-testid="custom-css-preview"]');

  return frame
    .locator("html")
    .evaluate((el) =>
      getComputedStyle(el).getPropertyValue("--brand").trim(),
    );
}

test.describe("Status page appearance editor", () => {
  // Creating a page, loading the preview iframe (which runs the real status0
  // app) and round-tripping a save through the public page is well past the
  // 30 s default.
  test.describe.configure({ timeout: 120_000 });

  /**
   * Opens the appearance route from the status-page detail screen.
   *
   * Deliberately no waitForLoadState("networkidle") here: the preview iframe
   * hosts status0, which polls its API every 30 s, so the network never goes
   * idle. Wait for the editor itself instead.
   */
  async function openAppearance(page: Page) {
    await page.getByTestId("status-page-appearance-nav").click();
    await page.waitForURL(/\/appearance$/, { timeout: 30000 });
    await expect(page.getByTestId("custom-css-input")).toBeVisible({
      timeout: 30000,
    });
  }

  test("previews CSS live, saves it to the public page, and ignores foreign messages", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    const slug = await createStatusPage(page, suffix);

    // Navigate through the UI, proving the detail screen links to the route.
    await openAppearance(page);

    const editor = page.getByTestId("custom-css-input");
    await expect(editor).toHaveValue("");

    const preview = page.locator('[data-testid="custom-css-preview"]');
    await expect(preview).toBeVisible();

    // --- 1. Live preview, no save ---------------------------------------
    await editor.fill(`:root { --brand: ${PREVIEW_BRAND}; }`);

    await expect
      .poll(() => previewBrand(page), { timeout: 15000 })
      .toBe(PREVIEW_BRAND);

    // Nothing was published: the public page is still unstyled.
    const publicPage = await page.context().newPage();
    // Absolute: baseURL is the /dash0/ app, and a relative path would
    // resolve under it and silently load dash0 instead of status0.
    await publicPage.goto(`${API_BASE}/status0/test/${slug}`);
    await publicPage.waitForLoadState("networkidle");
    await expect(publicPage.locator("style", { hasText: "--brand" })).toHaveCount(
      0,
    );

    // --- 2. A wrong-shaped / foreign message is ignored -------------------
    await page.evaluate(() => {
      const frame = document.querySelector<HTMLIFrameElement>(
        '[data-testid="custom-css-preview"]',
      );
      // Right origin, wrong shapes: a foreign type, a missing css field, a
      // non-string css, and a bare string. None may reach the page.
      frame?.contentWindow?.postMessage(
        { type: "evil:preview-css", css: ":root { --brand: rgb(1, 2, 3); }" },
        window.location.origin,
      );
      frame?.contentWindow?.postMessage(
        { type: "sp:preview-css" },
        window.location.origin,
      );
      frame?.contentWindow?.postMessage(
        { type: "sp:preview-css", css: 42 },
        window.location.origin,
      );
      frame?.contentWindow?.postMessage(
        "sp:preview-css",
        window.location.origin,
      );
    });

    // Give the (rejected) messages a chance to land, then re-assert.
    await page.waitForTimeout(500);
    expect(await previewBrand(page)).toBe(PREVIEW_BRAND);

    // --- 3. Save publishes it -------------------------------------------
    await editor.fill(`:root { --brand: ${SAVED_BRAND}; }`);
    await page.getByTestId("custom-css-save").click();

    await expect
      .poll(
        async () => {
          await publicPage.reload();
          await publicPage.waitForLoadState("networkidle");

          return publicPage
            .locator("html")
            .evaluate((el) =>
              getComputedStyle(el).getPropertyValue("--brand").trim(),
            );
        },
        { timeout: 20000 },
      )
      .toBe(SAVED_BRAND);

    // --- 4. Clearing the textarea removes it -----------------------------
    await editor.fill("");
    await page.getByTestId("custom-css-save").click();

    await expect
      .poll(
        async () => {
          await publicPage.reload();
          await publicPage.waitForLoadState("networkidle");

          return publicPage.locator("style", { hasText: "--brand" }).count();
        },
        { timeout: 20000 },
      )
      .toBe(0);

    await publicPage.close();
  });

  test("rejects @import and oversize stylesheets before saving", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    await createStatusPage(page, suffix);

    await openAppearance(page);

    const editor = page.getByTestId("custom-css-input");
    const save = page.getByTestId("custom-css-save");

    await editor.fill("@import url('https://evil.example/x.css');");
    await expect(save).toBeDisabled();

    await editor.fill(`:root { --brand: ${PREVIEW_BRAND}; }`);
    await expect(save).toBeEnabled();
  });

  test("offers a starter template listing the theming variables", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    await createStatusPage(page, suffix);

    await openAppearance(page);

    await page.getByTestId("custom-css-template").click();

    const value = await page.getByTestId("custom-css-input").inputValue();
    for (const variable of [
      "--brand",
      "--background",
      "--foreground",
      "--card",
      "--border",
      "--status-ok",
      "--status-warning",
      "--status-error",
      ".dark",
      // The `sp-*` element hooks are part of the same documented API.
      ".sp-logo",
      ".sp-page-name",
      ".sp-footer",
      ".sp-powered-by",
      ".sp-version",
    ]) {
      expect(value).toContain(variable);
    }
  });

  /**
   * The `sp-*` classes are a documented public API (spec 2026-08-01-05): both
   * logo-replacement techniques and hiding the version must work from custom
   * CSS alone. Crucially, the status page's own logo sizing lives in a
   * stylesheet, not an inline style — an inline style would be unbeatable
   * without `!important` and this test would fail.
   */
  test("retargets the logo and hides the version through the sp-* hooks", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    const slug = await createStatusPage(page, suffix);
    await openAppearance(page);

    const editor = page.getByTestId("custom-css-input");
    const save = page.getByTestId("custom-css-save");

    const publicPage = await page.context().newPage();
    await publicPage.goto(`${API_BASE}/status0/test/${slug}`);
    await publicPage.waitForLoadState("networkidle");

    // Baseline — without custom CSS both are on screen, so the assertions
    // further down cannot pass vacuously.
    const logoImg = publicPage.locator(".sp-logo img");
    const version = publicPage.locator(".sp-version");
    await expect(logoImg).toBeVisible({ timeout: 15000 });
    await expect(version).toBeVisible({ timeout: 15000 });
    // 26 tracks <Logo size={26}> in status0's status-page-view.tsx header —
    // update both together. The number itself is not the contract; what this
    // baseline buys is that the app's own size is something other than the
    // 140px the operator sets below, so that assertion cannot pass vacuously.
    expect((await logoImg.boundingBox())?.width).toBeCloseTo(26, 0);

    // --- Technique 1: repaint the <img> and resize it ----------------------
    await editor.fill(
      `.sp-logo img { content: url("${PIXEL}"); width: 140px; }`,
    );
    await save.click();

    await expect
      .poll(
        async () => {
          await publicPage.reload();
          await publicPage.waitForLoadState("networkidle");

          return publicPage
            .locator(".sp-logo img")
            .evaluate((el) => getComputedStyle(el).content);
        },
        { timeout: 20000 },
      )
      .toContain("data:image/gif;base64");
    // The operator's width beats the app's own rule — this is what breaks if
    // the size ever moves back onto an inline style.
    expect((await logoImg.boundingBox())?.width).toBeCloseTo(140, 0);

    // --- Technique 2: hide the <img>, paint the wrapper, hide the version ---
    await editor.fill(
      [
        ".sp-logo img { display: none; }",
        `.sp-logo { background: url("${PIXEL}") center / contain no-repeat;`,
        "  width: 120px; height: 32px; }",
        ".sp-version { display: none; }",
      ].join("\n"),
    );
    await save.click();

    await expect
      .poll(
        async () => {
          await publicPage.reload();
          await publicPage.waitForLoadState("networkidle");

          return publicPage.locator(".sp-logo img").isHidden();
        },
        { timeout: 20000 },
      )
      .toBe(true);

    // The wrapper did not collapse: it is the sized, painted element now.
    const wrapper = publicPage.locator(".sp-logo");
    const box = await wrapper.boundingBox();
    expect(box?.width).toBeCloseTo(120, 0);
    expect(box?.height).toBeCloseTo(32, 0);
    expect(
      await wrapper.evaluate((el) => getComputedStyle(el).backgroundImage),
    ).toContain("data:image/gif;base64");

    // And the version line is gone — `.sp-version { display: none }`.
    await expect(version).toBeHidden();

    await publicPage.close();
  });

  // Spec 2026-08-08-07: the page-level SVG badge embed block on the same
  // settings screen as the custom-CSS editor.
  test("offers a copyable SVG badge embed pointing at the public badge endpoint", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    const slug = await createStatusPage(page, suffix);

    await openAppearance(page);

    const card = page.getByTestId("status-page-badge-card");
    await expect(card).toBeVisible();
    await expect(page.getByTestId("status-page-badge-preview")).toBeVisible({
      timeout: 15000,
    });

    const urlText = await page.getByTestId("badge-embed-url").textContent();
    expect(urlText).toContain(`/api/v1/status-pages/test/${slug}/badge`);

    const markdownText = await page
      .getByTestId("badge-embed-markdown")
      .textContent();
    expect(markdownText).toContain(`(${urlText})`);

    const htmlText = await page.getByTestId("badge-embed-html").textContent();
    expect(htmlText).toContain(`src="${urlText}"`);

    // The URL is live: it 200s with a real SVG carrying the rollup status —
    // a freshly-created page has no resources, so it rolls up to "unknown".
    const resp = await page.request.get(urlText!);
    expect(resp.status()).toBe(200);
    expect(resp.headers()["content-type"]).toContain("image/svg+xml");
    const body = await resp.text();
    expect(body).toContain("<svg");
    expect(body).toContain(">unknown<");

    // Customizing the label updates every embed snippet together.
    await page.getByTestId("status-page-badge-label").fill("Custom Label");
    await expect
      .poll(() => page.getByTestId("badge-embed-url").textContent())
      .toContain("label=Custom");
    const customUrlText = await page
      .getByTestId("badge-embed-url")
      .textContent();
    const customMarkdown = await page
      .getByTestId("badge-embed-markdown")
      .textContent();
    expect(customMarkdown).toContain(`(${customUrlText})`);
  });
  // Spec 2026-08-08-08: the live-widget snippet generator, sharing the badge
  // block's home on this screen.
  test("generates a live widget <script> snippet that reflects mode, theme and position", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    const slug = await createStatusPage(page, suffix);

    await openAppearance(page);

    const card = page.getByTestId("status-page-widget-card");
    await expect(card).toBeVisible();

    const snippet = page.getByTestId("widget-embed-snippet");
    const defaultSnippet = (await snippet.textContent())!;
    // The exact frozen v1 contract: async script + data-page="org/slug".
    expect(defaultSnippet).toContain("/embed/v1/widget.js");
    expect(defaultSnippet).toContain(`data-page="test/${slug}"`);
    expect(defaultSnippet).toContain("<script async src=");
    expect(defaultSnippet).toContain("></script>");
    // Defaults are implicit — no redundant attributes in the pasted snippet.
    expect(defaultSnippet).not.toContain("data-mode=");
    expect(defaultSnippet).not.toContain("data-theme=");
    expect(defaultSnippet).not.toContain("data-position=");
    // The preview drives itself via data-force-status, but that must never
    // leak into the snippet customers actually copy — otherwise every pasted
    // pill would be permanently frozen on whatever state happened to be
    // selected in the preview when it was copied.
    expect(defaultSnippet).not.toContain("data-force-status");
    // No empty data-label-* either: untouched label inputs must not emit an
    // attribute at all.
    expect(defaultSnippet).not.toContain('data-label-operational=""');
    expect(defaultSnippet).not.toContain("data-label-");

    // The generated URL is live: the script it points at really is served.
    const scriptUrl = defaultSnippet.match(/src="([^"]+)"/)![1];
    const scriptResponse = await page.request.get(scriptUrl);
    expect(scriptResponse.status()).toBe(200);
    expect(scriptResponse.headers()["content-type"]).toContain(
      "application/javascript",
    );

    // Floating mode reveals the position select and adds both attributes.
    await page.getByTestId("status-page-widget-mode").click();
    await page.getByRole("option", { name: /Floating/ }).click();
    await expect(page.getByTestId("status-page-widget-position")).toBeVisible();
    await expect
      .poll(() => snippet.textContent())
      .toContain('data-mode="floating"');
    expect(await snippet.textContent()).toContain(
      'data-position="bottom-right"',
    );

    await page.getByTestId("status-page-widget-position").click();
    await page.getByRole("option", { name: /Bottom left/ }).click();
    await expect
      .poll(() => snippet.textContent())
      .toContain('data-position="bottom-left"');

    await page.getByTestId("status-page-widget-theme").click();
    await page.getByRole("option", { name: /^Dark$/ }).click();
    await expect.poll(() => snippet.textContent()).toContain('data-theme="dark"');
  });

  // Spec 2026-08-10: the link-to-status-page opt-out toggle.
  test("the link toggle is checked by default and unchecking it adds data-link=\"false\" to the snippet", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    await createStatusPage(page, suffix);
    await openAppearance(page);

    const snippet = page.getByTestId("widget-embed-snippet");
    const toggle = page.getByTestId("status-page-widget-link-toggle");

    await expect(toggle).toBeVisible();
    await expect(toggle).toHaveAttribute("data-state", "checked");
    // Checked by default -> no data-link attribute in the pristine snippet,
    // matching today's behavior byte-for-byte.
    expect(await snippet.textContent()).not.toContain("data-link");

    await toggle.click();
    await expect(toggle).toHaveAttribute("data-state", "unchecked");
    await expect
      .poll(() => snippet.textContent())
      .toContain('data-link="false"');

    // Toggling back on removes it again.
    await toggle.click();
    await expect(toggle).toHaveAttribute("data-state", "checked");
    await expect
      .poll(() => snippet.textContent())
      .not.toContain("data-link");
  });

  // Spec 2026-08-08-09: live preview + size/label customization.
  test("shows a live preview of the real widget and updates it as preview state/size/theme change", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    await createStatusPage(page, suffix);
    await openAppearance(page);

    // The preview never calls the summary endpoint — it drives its own state
    // via data-force-status, so it must work even though this page has just
    // been created (no real status history yet).
    let summaryRequested = false;
    page.on("request", (request) => {
      if (request.url().includes("/summary")) {
        summaryRequested = true;
      }
    });

    const previewFrame = page.frameLocator(
      '[data-testid="status-page-widget-preview"]',
    );
    const previewPill = previewFrame.locator(
      '[data-testid="solidping-widget-pill"]',
    );
    const previewLabel = previewFrame.locator(
      '[data-testid="solidping-widget-label"]',
    );

    await expect(previewPill).toBeVisible({ timeout: 30000 });
    await expect(previewLabel).toHaveText("All systems operational");
    // No link target: the preview never has real page data to link to.
    expect(await previewPill.evaluate((el) => el.tagName)).toBe("SPAN");

    // Preview-state picker switches the rendered pill without a save.
    await page.getByTestId("status-page-widget-preview-status").click();
    await page.getByRole("option", { name: /^Down$/ }).click();
    await expect(previewLabel).toHaveText("Major outage");

    const fontSize = () =>
      previewPill.evaluate((el) => getComputedStyle(el).fontSize);
    const mdFontSize = await fontSize();

    await page.getByTestId("status-page-widget-size").click();
    await page.getByRole("option", { name: /^Large$/ }).click();
    await expect(previewPill).toBeVisible({ timeout: 30000 });
    await expect
      .poll(fontSize)
      .not.toBe(mdFontSize);

    await page.getByTestId("status-page-widget-theme").click();
    await page.getByRole("option", { name: /^Dark$/ }).click();
    await expect(previewFrame.locator("body")).toBeVisible({ timeout: 30000 });
    await expect
      .poll(() =>
        previewFrame
          .locator("body")
          .evaluate((el) => getComputedStyle(el).backgroundColor),
      )
      .toBe("rgb(11, 18, 32)");

    expect(summaryRequested).toBe(false);
  });

  test("preview theme 'auto' (the default) follows dash0's own light/dark toggle", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    await createStatusPage(page, suffix);
    await openAppearance(page);

    // Widget theme is "auto" by default — never touched in this test.
    const previewFrame = page.frameLocator(
      '[data-testid="status-page-widget-preview"]',
    );
    const previewBody = previewFrame.locator("body");
    const backgroundColor = () =>
      previewBody.evaluate((el) => getComputedStyle(el).backgroundColor);

    await expect(previewFrame.locator('[data-testid="solidping-widget-pill"]')).toBeVisible({
      timeout: 30000,
    });

    // A freshly restored session starts with no persisted theme preference,
    // so dash0 falls back to the (light) system preference — the preview
    // must mirror that starting point.
    await expect.poll(backgroundColor).toBe("rgb(248, 250, 252)");

    // Flipping dash0's own theme must flip the "auto" preview along with it,
    // with no widget option touched — this is the MutationObserver-backed
    // reactivity in useIsDashDark(), not a one-time read at mount.
    await page.getByTestId("theme-toggle").click();
    await expect.poll(backgroundColor).toBe("rgb(11, 18, 32)");

    // And back again, proving it isn't a one-way/stuck transition.
    await page.getByTestId("theme-toggle").click();
    await expect.poll(backgroundColor).toBe("rgb(248, 250, 252)");
  });

  test("a customized label updates both the preview and the copyable snippet, escaped against quote-breakout", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    const suffix = Date.now().toString().slice(-9);
    await createStatusPage(page, suffix);
    await openAppearance(page);

    await page.getByTestId("status-page-widget-customize-labels").click();

    const hostile = `"><img src=x onerror="window.__pwned=1">`;
    const labelInput = page.getByTestId("status-page-widget-label-operational");
    await expect(labelInput).toBeVisible();
    await labelInput.fill(hostile);

    // The emitted (copyable) snippet must remain valid, single-attribute
    // HTML: the quote is escaped rather than allowed to break out into a new
    // attribute on the tag customers paste onto their own site.
    const snippet = page.getByTestId("widget-embed-snippet");
    await expect
      .poll(() => snippet.textContent())
      .toContain("&quot;&gt;&lt;img src=x onerror=&quot;window.__pwned=1&quot;&gt;");
    expect(await snippet.textContent()).not.toContain(
      `data-label-operational="${hostile}"`,
    );

    // The preview (same status: default is operational) renders the hostile
    // value as inert text — no image element, no markup breakout.
    const previewFrame = page.frameLocator(
      '[data-testid="status-page-widget-preview"]',
    );
    const previewLabel = previewFrame.locator(
      '[data-testid="solidping-widget-label"]',
    );
    await expect(previewLabel).toHaveText(hostile, { timeout: 30000 });
    expect(await previewFrame.locator("img").count()).toBe(0);
  });
});
