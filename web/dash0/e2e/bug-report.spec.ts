import { test, expect, type Page } from "./fixtures";

// In-app bug reports — covers the four scenarios from the spec:
// desktop happy path, keyboard shortcut, mobile layout via dispatched
// event, and feature flag off (icon hidden, shortcut no-op).

// Merge bugReport into the real /api/v1/config body so every other capability
// stays as the server reports it (spec 2026-09-29-05).
async function stubBugReport(page: Page, enabled: boolean) {
  await page.route("**/api/v1/config", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    await route.fulfill({
      response,
      json: { ...body, bugReport: { enabled } },
    });
  });
}

// The `feedback:open` listener is registered by an effect that only runs once
// /api/v1/config said bugReport.enabled, and `networkidle` does not guarantee
// React applied it: a one-shot event dispatched too early is lost. The bug icon
// renders in the same render as the listener's registration, so wait for it.
//
// The dialog also opens only once the screenshot is taken (html-to-image clones
// and serializes the whole page). Measured with 16 workers on 10 cores that
// takes 1.5 to 6.5 s, past the default 5 s expect timeout, so the wait for the
// dialog is bound to the capture, not to a UI transition.
const CAPTURE_TIMEOUT = 30_000;

async function openFeedback(page: Page) {
  await expect(page.getByTestId("feedback-button")).toBeVisible();
  await page.evaluate(() => window.dispatchEvent(new Event("feedback:open")));
  await expect(page.getByTestId("feedback-comment")).toBeVisible({ timeout: CAPTURE_TIMEOUT });
}

test.describe("Bug report", () => {
  test("desktop happy path: opens dialog, captures, submits payload", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Force bugReport.enabled=true so the icon renders even when the
    // backend hasn't been configured with a GitHub token.
    await stubBugReport(page, true);

    // Stub the report endpoint so the test passes regardless of GitHub
    // connectivity.
    let submittedFormFields: string[] = [];
    await page.route("**/api/mgmt/report", async (route) => {
      const body = route.request().postData() || "";
      submittedFormFields = body.split("\r\n").filter(Boolean);
      await route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
    });

    await page.reload();

    await page.getByTestId("feedback-button").click();

    const comment = page.getByTestId("feedback-comment");
    await comment.waitFor({ state: "visible", timeout: CAPTURE_TIMEOUT });
    await comment.fill("Found a bug on the dashboard");

    await page.getByTestId("feedback-submit").click();

    await expect.poll(() => submittedFormFields.length).toBeGreaterThan(0);
    expect(submittedFormFields.join("\n")).toContain(
      "Found a bug on the dashboard",
    );
  });

  test("keyboard shortcut opens the dialog", async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    await stubBugReport(page, true);
    await page.reload();
    await page.waitForLoadState("networkidle");

    const isMac = await page.evaluate(() => /Mac/.test(navigator.platform));
    const modifier = isMac ? "Meta" : "Control";
    await page.keyboard.press(`${modifier}+Shift+B`);

    await expect(page.getByTestId("feedback-comment")).toBeVisible({ timeout: CAPTURE_TIMEOUT });
  });

  test("mobile layout (event-triggered): textarea and annotation toolbar visible", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 667 });

    await stubBugReport(page, true);
    await page.reload();
    await page.waitForLoadState("networkidle");

    await openFeedback(page);

    await expect(page.getByTestId("feedback-comment")).toBeVisible();
    await expect(page.getByTestId("annotation-toolbar")).toBeVisible();
    await expect(page.getByTestId("annotation-canvas")).toBeVisible();
  });

  test("feature off: bug icon is not rendered and shortcut is a no-op", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await stubBugReport(page, false);
    await page.reload();
    await page.waitForLoadState("networkidle");

    await expect(page.getByTestId("feedback-button")).toBeHidden();

    const isMac = await page.evaluate(() => /Mac/.test(navigator.platform));
    const modifier = isMac ? "Meta" : "Control";
    await page.keyboard.press(`${modifier}+Shift+B`);

    await page.waitForTimeout(200);
    await expect(page.getByTestId("feedback-comment")).toBeHidden();
  });
});

// Annotation tools (spec 2026-10-09-01).
async function openDialog(page: Page) {
  await stubBugReport(page, true);
  await page.reload();
  await page.waitForLoadState("networkidle");
  await openFeedback(page);
  await expect(page.getByTestId("annotation-canvas")).toBeVisible();
  // Wait for the screenshot to be painted on the overlay.
  await page.waitForTimeout(300);
}

async function drag(
  page: Page,
  from: [number, number],
  to: [number, number],
) {
  const box = await page.getByTestId("annotation-canvas").boundingBox();
  if (!box) throw new Error("no canvas");
  await page.mouse.move(box.x + box.width * from[0], box.y + box.height * from[1]);
  await page.mouse.down();
  await page.mouse.move(
    box.x + box.width * ((from[0] + to[0]) / 2),
    box.y + box.height * ((from[1] + to[1]) / 2),
  );
  await page.mouse.move(box.x + box.width * to[0], box.y + box.height * to[1]);
  await page.mouse.up();
}

async function clickAt(page: Page, at: [number, number]) {
  const box = await page.getByTestId("annotation-canvas").boundingBox();
  if (!box) throw new Error("no canvas");
  await page.mouse.click(box.x + box.width * at[0], box.y + box.height * at[1]);
}

// submitAndGetPNG submits the dialog and returns the screenshot as base64.
async function submitAndGetPNG(page: Page): Promise<string> {
  let png = "";
  await page.route("**/api/mgmt/report", async (route) => {
    const buf = route.request().postDataBuffer();
    if (buf) {
      const idx = buf.indexOf(Buffer.from([0x89, 0x50, 0x4e, 0x47]));
      const end = buf.lastIndexOf(Buffer.from("\r\n--"));
      png = buf.subarray(idx, end > idx ? end : undefined).toString("base64");
    }
    await route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
  });
  await page.getByTestId("feedback-submit").click();
  await expect.poll(() => png.length).toBeGreaterThan(0);
  return png;
}

async function countRed(page: Page, b64: string): Promise<number> {
  return page.evaluate(async (data) => {
    const bytes = Uint8Array.from(atob(data), (c) => c.charCodeAt(0));
    const bmp = await createImageBitmap(new Blob([bytes], { type: "image/png" }));
    const c = document.createElement("canvas");
    c.width = bmp.width;
    c.height = bmp.height;
    const ctx = c.getContext("2d");
    if (!ctx) return 0;
    ctx.drawImage(bmp, 0, 0);
    const d = ctx.getImageData(0, 0, c.width, c.height).data;
    let n = 0;
    for (let i = 0; i < d.length; i += 4) {
      if (d[i] > 200 && d[i + 1] < 80 && d[i + 2] < 80) n++;
    }
    return n;
  }, b64);
}

test.describe("Bug report annotations", () => {
  test("mobile: drawing a rect with pointer events changes the posted screenshot", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 667 });
    await openDialog(page);
    await expect(page.getByTestId("annotation-toolbar")).toBeVisible();

    await page.getByTestId("tool-rect").click();
    await drag(page, [0.2, 0.2], [0.7, 0.7]);

    const annotated = await submitAndGetPNG(page);
    const plain = await (async () => {
      await openFeedback(page);
      await expect(page.getByTestId("annotation-canvas")).toBeVisible();
      await page.waitForTimeout(300);
      return submitAndGetPNG(page);
    })();
    expect(await countRed(page, annotated)).toBeGreaterThan(await countRed(page, plain));
  });

  test("text tool: inline input, no window.prompt, Enter commits, Escape cancels", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    let dialogs = 0;
    page.on("dialog", (d) => {
      dialogs++;
      void d.dismiss();
    });
    await openDialog(page);

    await page.getByTestId("tool-text").click();
    await clickAt(page, [0.3, 0.5]);
    const input = page.getByTestId("annotation-text-input");
    await expect(input).toBeVisible();
    await input.fill("Look here");
    await input.press("Enter");
    await expect(input).toBeHidden();
    await expect(page.getByTestId("annotation-undo")).toBeEnabled();

    // Cancel path adds nothing.
    await page.getByTestId("annotation-undo").click();
    await expect(page.getByTestId("annotation-undo")).toBeDisabled();
    await clickAt(page, [0.3, 0.5]);
    await expect(input).toBeVisible();
    await input.fill("Discard me");
    await input.press("Escape");
    await expect(input).toBeHidden();
    await expect(page.getByTestId("annotation-undo")).toBeDisabled();
    expect(dialogs).toBe(0);
  });

  test("pen, highlight and blur each add one annotation; undo/redo round trip", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await openDialog(page);

    const undo = page.getByTestId("annotation-undo");
    const redo = page.getByTestId("annotation-redo");
    await expect(undo).toBeDisabled();
    await expect(redo).toBeDisabled();

    for (const tool of ["pen", "highlight", "blur"]) {
      await page.getByTestId(`tool-${tool}`).click();
      await drag(page, [0.2, 0.2], [0.5, 0.4]);
      await expect(undo).toBeEnabled();
      await undo.click();
      await expect(undo).toBeDisabled();
      await expect(redo).toBeEnabled();
      await redo.click();
      await expect(undo).toBeEnabled();
      await expect(redo).toBeDisabled();
      await undo.click();
      await expect(undo).toBeDisabled();
      await page.getByTestId("annotation-redo").click();
      await undo.click();
      // Leave redo history clean for the next tool: a new edit clears it.
    }
  });

  test("blur redacts pixels in the posted screenshot", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await openDialog(page);
    await page.getByTestId("tool-blur").click();
    await drag(page, [0.0, 0.0], [1.0, 1.0]);
    const png = await submitAndGetPNG(page);
    // A fully pixelated image has far fewer distinct 2x2 neighbours than
    // the original: check that adjacent pixels inside a block are equal.
    const uniformShare = await page.evaluate(async (data) => {
      const bytes = Uint8Array.from(atob(data), (c) => c.charCodeAt(0));
      const bmp = await createImageBitmap(new Blob([bytes], { type: "image/png" }));
      const c = document.createElement("canvas");
      c.width = bmp.width;
      c.height = bmp.height;
      const ctx = c.getContext("2d");
      if (!ctx) return 0;
      ctx.drawImage(bmp, 0, 0);
      const d = ctx.getImageData(0, 0, c.width, c.height).data;
      let same = 0;
      let total = 0;
      for (let y = 0; y < c.height; y++) {
        for (let x = 1; x < c.width; x++) {
          const i = (y * c.width + x) * 4;
          const j = i - 4;
          total++;
          if (d[i] === d[j] && d[i + 1] === d[j + 1] && d[i + 2] === d[j + 2]) same++;
        }
      }
      return same / total;
    }, png);
    expect(uniformShare).toBeGreaterThan(0.8);
  });

  test("select + Delete removes an annotation; Delete with nothing selected is a no-op", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await openDialog(page);
    const undo = page.getByTestId("annotation-undo");

    await page.getByTestId("tool-rect").click();
    await drag(page, [0.2, 0.2], [0.5, 0.5]);
    await expect(undo).toBeEnabled();

    await page.getByTestId("tool-select").click();
    // Nothing selected: click empty area, press Delete.
    await clickAt(page, [0.9, 0.9]);
    await page.keyboard.press("Delete");
    await expect(undo).toBeEnabled();

    // Select and delete.
    await clickAt(page, [0.35, 0.35]);
    await page.keyboard.press("Delete");
    await expect(undo).toBeDisabled();
  });

  test("select + drag moves an annotation", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await openDialog(page);
    await page.getByTestId("tool-rect").click();
    await drag(page, [0.1, 0.1], [0.3, 0.3]);
    await page.getByTestId("tool-select").click();
    await drag(page, [0.2, 0.2], [0.7, 0.7]);
    // Moved away: the old spot no longer selects it, so Delete is a no-op.
    await clickAt(page, [0.15, 0.15]);
    await page.keyboard.press("Delete");
    await expect(page.getByTestId("annotation-undo")).toBeEnabled();
    // The new spot does.
    await clickAt(page, [0.7, 0.7]);
    await page.keyboard.press("Delete");
    await expect(page.getByTestId("annotation-undo")).toBeDisabled();
  });

  test("canvas charts appear in the capture; a tainted canvas does not break it", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await stubBugReport(page, true);
    await page.reload();
    await page.waitForLoadState("networkidle");

    await page.evaluate(() => {
      const chart = document.createElement("canvas");
      chart.width = 100;
      chart.height = 100;
      chart.style.cssText = "position:fixed;left:0;top:0;width:100px;height:100px;z-index:99999";
      const ctx = chart.getContext("2d");
      if (ctx) {
        ctx.fillStyle = "#ff0000";
        ctx.fillRect(0, 0, 100, 100);
      }
      document.body.appendChild(chart);

      const tainted = document.createElement("canvas");
      tainted.width = 50;
      tainted.height = 50;
      tainted.style.cssText = "position:fixed;right:0;bottom:0;width:50px;height:50px";
      tainted.toDataURL = () => {
        throw new DOMException("tainted", "SecurityError");
      };
      document.body.appendChild(tainted);
    });

    await openFeedback(page);

    await expect(page.getByTestId("feedback-comment")).toBeVisible({ timeout: CAPTURE_TIMEOUT });
    await expect(page.getByTestId("annotation-container")).toBeVisible();

    const pixel = await page.evaluate(async () => {
      const img = document.querySelector<HTMLImageElement>(
        '[data-testid="annotation-container"] img',
      );
      if (!img) return null;
      await img.decode();
      const ratio = img.naturalWidth / window.innerWidth;
      const c = document.createElement("canvas");
      c.width = img.naturalWidth;
      c.height = img.naturalHeight;
      const ctx = c.getContext("2d");
      if (!ctx) return null;
      ctx.drawImage(img, 0, 0);
      return Array.from(ctx.getImageData(Math.round(50 * ratio), Math.round(50 * ratio), 1, 1).data);
    });
    expect(pixel).not.toBeNull();
    expect(pixel?.[0]).toBeGreaterThan(200);
    expect(pixel?.[1]).toBeLessThan(80);

    // The live DOM is restored: the original canvases are visible again.
    await expect(page.locator("canvas[style*='z-index:99999'], canvas[style*='z-index: 99999']")).toBeVisible();
  });
});

// Viewport capture (spec 2026-10-11-01). A fixture with colored bands at known
// document offsets is injected, the page is scrolled, and the preview image in
// the dialog is sampled. Points are in viewport CSS pixels.
type RGB = [number, number, number];

async function injectViewportFixture(page: Page) {
  await page.evaluate(() => {
    const fixture = document.createElement("div");
    fixture.id = "vp-fixture";
    fixture.style.cssText = "position:relative;height:4000px";
    fixture.innerHTML = `
      <div style="position:absolute;top:0;left:0;right:0;height:200px;background:#ff0000"></div>
      <div style="position:absolute;top:2000px;left:0;right:0;height:200px;background:#00ff00"></div>
      <div id="vp-box" style="position:absolute;top:2300px;left:50%;width:150px;height:150px;overflow:auto;background:#ffffff">
        <div style="position:relative;height:2000px">
          <div style="position:absolute;top:1000px;left:0;right:0;height:150px;background:#ffff00"></div>
        </div>
      </div>`;
    document.body.prepend(fixture);
    const fixed = document.createElement("div");
    fixed.style.cssText =
      "position:fixed;right:0;bottom:0;width:60px;height:60px;background:#0000ff;z-index:2147483647";
    document.body.append(fixed);
  });
}

async function captureAndSample(page: Page, points: Record<string, [number, number]>) {
  await openFeedback(page);
  const img = page.getByTestId("annotation-container").locator("img");
  await expect(img).toBeVisible();
  await expect.poll(() => img.evaluate((el: HTMLImageElement) => el.naturalWidth)).toBeGreaterThan(0);
  const result = await img.evaluate(async (el: HTMLImageElement, pts) => {
    // Drawn from the element: the page CSP does not allow fetching blob: URLs.
    const bmp = await createImageBitmap(el);
    const canvas = document.createElement("canvas");
    canvas.width = bmp.width;
    canvas.height = bmp.height;
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("no 2d context");
    ctx.drawImage(bmp, 0, 0);
    const ratio = bmp.width / window.innerWidth;
    const samples: Record<string, [number, number, number]> = {};
    for (const [name, [x, y]] of Object.entries(pts)) {
      const d = ctx.getImageData(Math.round(x * ratio), Math.round(y * ratio), 1, 1).data;
      samples[name] = [d[0], d[1], d[2]];
    }
    return {
      width: bmp.width,
      height: bmp.height,
      expectedWidth: Math.round(window.innerWidth * Math.min(window.devicePixelRatio || 1, 2)),
      expectedHeight: Math.round(window.innerHeight * Math.min(window.devicePixelRatio || 1, 2)),
      samples,
    };
  }, points);
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("annotation-container")).toBeHidden();
  return result;
}

const isRed = ([r, g, b]: RGB) => r > 200 && g < 60 && b < 60;
const isGreen = ([r, g, b]: RGB) => g > 200 && r < 60 && b < 60;
const isBlue = ([r, g, b]: RGB) => b > 200 && r < 60 && g < 60;
const isYellow = ([r, g, b]: RGB) => r > 200 && g > 200 && b < 60;

test.describe("Bug report viewport capture", () => {
  async function setup(page: Page) {
    await stubBugReport(page, true);
    await page.reload();
    await page.waitForLoadState("networkidle");
    await injectViewportFixture(page);
  }

  function points(page: Page) {
    const vp = page.viewportSize();
    if (!vp) throw new Error("no viewport");
    return {
      top: [vp.width * 0.75, 20] as [number, number],
      fixed: [vp.width - 30, vp.height - 30] as [number, number],
    };
  }

  test("scrolled page: the image is the viewport, not the top of the page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await setup(page);

    // Positive control: unscrolled, the red band is at the top.
    const atTop = await captureAndSample(page, points(page));
    expect(atTop.width).toBe(atTop.expectedWidth);
    expect(atTop.height).toBe(atTop.expectedHeight);
    expect(isRed(atTop.samples.top)).toBe(true);
    expect(isBlue(atTop.samples.fixed)).toBe(true);

    await page.evaluate(() => window.scrollTo(0, 2000));
    const scrolled = await captureAndSample(page, points(page));
    expect(scrolled.width).toBe(scrolled.expectedWidth);
    expect(scrolled.height).toBe(scrolled.expectedHeight);
    expect(isGreen(scrolled.samples.top)).toBe(true);
    expect(isBlue(scrolled.samples.fixed)).toBe(true);
  });

  test("bottom of the page: full viewport height, not the top of the page", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await setup(page);
    await page.evaluate(() => window.scrollTo(0, 1e6));

    const result = await captureAndSample(page, points(page));
    expect(result.width).toBe(result.expectedWidth);
    expect(result.height).toBe(result.expectedHeight);
    expect(isRed(result.samples.top)).toBe(false);
    expect(isBlue(result.samples.fixed)).toBe(true);
  });

  test("scrolled inner container keeps its offset and is restored", async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;
    await setup(page);
    await page.evaluate(() => window.scrollTo(0, 2000));
    const boxCenter = async (): Promise<[number, number]> => {
      const box = await page.locator("#vp-box").boundingBox();
      if (!box) throw new Error("no box");
      return [box.x + box.width / 2, box.y + box.height / 2];
    };

    // Control: the box at scroll 0 shows white, not the yellow band.
    const plain = await captureAndSample(page, { box: await boxCenter() });
    expect(isYellow(plain.samples.box)).toBe(false);

    await page.locator("#vp-box").evaluate((el) => {
      el.scrollTop = 1000;
    });
    const scrolled = await captureAndSample(page, { box: await boxCenter() });
    expect(isYellow(scrolled.samples.box)).toBe(true);

    // The live page is put back as it was.
    expect(await page.locator("#vp-box").evaluate((el) => el.scrollTop)).toBe(1000);
    expect(await page.evaluate(() => window.scrollY)).toBe(2000);
  });

  test("mobile: scrolled capture is the viewport", async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.setViewportSize({ width: 375, height: 667 });
    await setup(page);
    await page.evaluate(() => window.scrollTo(0, 2000));

    const result = await captureAndSample(page, points(page));
    expect(result.width).toBe(result.expectedWidth);
    expect(result.height).toBe(result.expectedHeight);
    expect(isGreen(result.samples.top)).toBe(true);
    expect(isBlue(result.samples.fixed)).toBe(true);
  });
});
