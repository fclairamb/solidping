/**
 * The burned-in lower thirds, rasterised in the browser.
 *
 * The cut has no sound and is played muted in a README and on a docs page, so
 * the four beats have to name themselves on screen. ffmpeg's `drawtext` is the
 * obvious way to burn in a caption and is **not usable here**: it needs an
 * ffmpeg built with libfreetype, and Homebrew's current bottle is not one
 * (`ffmpeg -filters | grep drawtext` comes back empty on the machine that
 * actually regenerates this media). Requiring a custom ffmpeg build to re-cut a
 * demo video is a worse dependency than the one this pipeline already has.
 *
 * So the captions are drawn by the browser Playwright already ships, exported
 * as transparent PNGs, and composited with `overlay`. Beyond working, that
 * gives them the product's own typography rather than whatever face a font
 * lookup happened to find.
 */
import { chromium } from "@playwright/test";
import { mkdir } from "node:fs/promises";
import path from "node:path";

export interface LabelImage {
  text: string;
  file: string;
  width: number;
  height: number;
}

/** Height of the caption box, so the y position can be a constant. */
const FONT_SIZE = 30;

/**
 * Slug for the PNG filename. Not shown anywhere — it only has to be stable and
 * filesystem-safe, so two runs of the same cut overwrite rather than accumulate.
 */
function slug(text: string): string {
  const cleaned = text
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "");

  return cleaned === "" ? "label" : cleaned;
}

const CSS = `
  * { box-sizing: border-box; }
  html, body {
    margin: 0;
    padding: 0;
    background: transparent;
  }
  .pill {
    display: inline-block;
    font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto,
      "Helvetica Neue", Arial, sans-serif;
    font-size: ${FONT_SIZE}px;
    font-weight: 600;
    line-height: 1.2;
    letter-spacing: 0.005em;
    color: #ffffff;
    background: rgba(15, 23, 42, 0.82);
    border: 1px solid rgba(255, 255, 255, 0.14);
    border-radius: 14px;
    padding: 13px 26px 15px;
    white-space: nowrap;
  }
`;

/**
 * Draws one PNG per distinct caption into `dir`.
 *
 * Rendered at deviceScaleFactor 1 on purpose: the PNG is composited onto the
 * finished 1280×800 frame at its natural size, so one CSS pixel has to be one
 * video pixel. A 2× render would have to be scaled back down and would land
 * softer than text drawn at the size it is shown.
 */
export async function renderLabelImages(
  texts: string[],
  dir: string,
): Promise<Map<string, LabelImage>> {
  const distinct = [...new Set(texts)];
  const images = new Map<string, LabelImage>();
  if (distinct.length === 0) return images;

  await mkdir(dir, { recursive: true });

  const browser = await chromium.launch();
  try {
    const page = await browser.newPage({
      viewport: { width: 1280, height: 800 },
      deviceScaleFactor: 1,
      colorScheme: "light",
    });

    const body = distinct
      .map(
        (text, index) =>
          `<div class="pill" id="pill-${index}">${escapeHtml(text)}</div>`,
      )
      .join("\n");
    await page.setContent(
      `<!doctype html><meta charset="utf-8"><style>${CSS}</style>${body}`,
      { waitUntil: "load" },
    );
    // System fonts are already resident, but a caption measured mid-layout
    // would be clipped by a pixel or two on its right edge.
    await page.evaluate(() => document.fonts.ready);

    for (const [index, text] of distinct.entries()) {
      const pill = page.locator(`#pill-${index}`);
      const box = await pill.boundingBox();
      if (!box) {
        throw new Error(`The caption "${text}" did not render a box to capture.`);
      }
      const file = path.join(dir, `${slug(text)}.png`);
      await pill.screenshot({ path: file, omitBackground: true });
      images.set(text, {
        text,
        file,
        width: Math.round(box.width),
        height: Math.round(box.height),
      });
    }
  } finally {
    await browser.close();
  }

  return images;
}

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}
