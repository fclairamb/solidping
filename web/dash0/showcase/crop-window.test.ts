import { describe, expect, it } from "vitest";
import {
  buildZoompanExpressions,
  cropWindowAt,
  evaluateSum,
  isIdentitySeries,
  renderSumExpression,
  resolveCues,
  smoothstep,
  sumSeries,
  type CueFile,
  type Frame,
} from "./crop-window";

const FRAME: Frame = { width: 2560, height: 1600 };
const VIEWPORT = { width: 1280, height: 800 };

function cueFile(cues: CueFile["cues"]): CueFile {
  return { version: 1, viewport: VIEWPORT, cues };
}

/** Samples the window every 40 ms (the source frame period) across a span. */
function sample(
  series: ReturnType<typeof resolveCues>,
  from: number,
  to: number,
): ReturnType<typeof cropWindowAt>[] {
  const out = [];
  for (let t = from; t <= to + 1e-9; t += 0.04) out.push(cropWindowAt(series, t));
  return out;
}

describe("smoothstep", () => {
  it("pins both ends and eases in between", () => {
    expect(smoothstep(0)).toBe(0);
    expect(smoothstep(1)).toBe(1);
    expect(smoothstep(0.5)).toBeCloseTo(0.5, 10);
    // Saturates outside [0, 1] — that is what makes the flat sum work.
    expect(smoothstep(-3)).toBe(0);
    expect(smoothstep(7)).toBe(1);
  });

  it("is monotone", () => {
    let previous = -1;
    for (let u = 0; u <= 1.0001; u += 0.01) {
      const value = smoothstep(u);
      expect(value).toBeGreaterThanOrEqual(previous);
      previous = value;
    }
  });
});

describe("no cues", () => {
  it("produces the identity crop at every time", () => {
    const series = resolveCues(cueFile([]), FRAME);
    for (const t of [0, 1, 5, 60]) {
      expect(cropWindowAt(series, t)).toEqual({
        x: 0,
        y: 0,
        width: 2560,
        height: 1600,
        zoom: 1,
      });
    }
  });

  it("renders as a no-op zoompan", () => {
    const series = resolveCues(cueFile([]), FRAME);
    expect(buildZoompanExpressions(series, "on/25")).toEqual({
      zoom: "1",
      x: "0",
      y: "0",
    });
    expect(isIdentitySeries(series)).toBe(true);
  });

  it("treats an all-full-frame cue list as identity too", () => {
    const series = resolveCues(
      cueFile([
        { t: 0, rect: null },
        { t: 4, rect: null },
      ]),
      FRAME,
    );
    expect(isIdentitySeries(series)).toBe(true);
    expect(cropWindowAt(series, 2).zoom).toBeCloseTo(1, 10);
  });
});

describe("aspect preservation", () => {
  it("keeps the source aspect ratio at every sampled instant", () => {
    const series = resolveCues(
      cueFile([
        { t: 0, rect: null },
        { t: 1, rect: { x: 100, y: 80, width: 160, height: 40 }, zoom: 1.6 },
        { t: 4, rect: { x: 900, y: 600, width: 300, height: 120 }, zoom: 1.35 },
        { t: 7, rect: null },
      ]),
      FRAME,
    );
    const sourceAspect = FRAME.width / FRAME.height;
    for (const window of sample(series, 0, 8)) {
      expect(window.width / window.height).toBeCloseTo(sourceAspect, 9);
    }
  });

  it("derives the zoom from the rect when none is given, capped at maxZoom", () => {
    const series = resolveCues(
      cueFile([{ t: 0, rect: { x: 0, y: 0, width: 10, height: 6 } }]),
      FRAME,
      { maxZoom: 1.8 },
    );
    expect(series.keys[0].zoom).toBeCloseTo(1.8, 10);
  });
});

describe("clamping at the frame edges", () => {
  const corners = [
    { name: "top-left", rect: { x: 0, y: 0, width: 40, height: 20 } },
    { name: "top-right", rect: { x: 1240, y: 0, width: 40, height: 20 } },
    { name: "bottom-left", rect: { x: 0, y: 780, width: 40, height: 20 } },
    { name: "bottom-right", rect: { x: 1240, y: 780, width: 40, height: 20 } },
  ];

  for (const corner of corners) {
    it(`keeps the window inside the source when framing the ${corner.name} corner`, () => {
      const series = resolveCues(
        cueFile([
          { t: 0, rect: null },
          { t: 1, rect: corner.rect, zoom: 1.8 },
          { t: 5, rect: null },
        ]),
        FRAME,
      );
      for (const w of sample(series, 0, 6)) {
        expect(w.x).toBeGreaterThanOrEqual(-1e-9);
        expect(w.y).toBeGreaterThanOrEqual(-1e-9);
        expect(w.x + w.width).toBeLessThanOrEqual(FRAME.width + 1e-9);
        expect(w.y + w.height).toBeLessThanOrEqual(FRAME.height + 1e-9);
      }
    });
  }

  it("never needs the per-frame clamp between two clamped cues", () => {
    // The concavity argument in crop-window.ts: interpolating between two
    // in-frame endpoints can never leave the frame. If it ever could, the
    // unclamped centre would drift past the limit and this would fail.
    const series = resolveCues(
      cueFile([
        { t: 0, rect: { x: 1240, y: 780, width: 40, height: 20 }, zoom: 1.05 },
        { t: 3, rect: { x: 0, y: 0, width: 40, height: 20 }, zoom: 1.8 },
      ]),
      FRAME,
      { defaultTransitionMs: 2000 },
    );
    const cxSeries = sumSeries(series.keys, (k) => k.cx);
    const zoomSeries = sumSeries(series.keys, (k) => k.zoom);
    for (let t = 0; t <= 3; t += 0.02) {
      const zoom = evaluateSum(zoomSeries, t);
      const half = FRAME.width / (2 * zoom);
      const cx = evaluateSum(cxSeries, t);
      expect(cx).toBeGreaterThanOrEqual(half - 1e-6);
      expect(cx).toBeLessThanOrEqual(FRAME.width - half + 1e-6);
    }
  });
});

describe("easing", () => {
  it("moves monotonically from one zoom to the next and holds after", () => {
    const series = resolveCues(
      cueFile([
        { t: 0, rect: null },
        { t: 2, rect: { x: 500, y: 400, width: 200, height: 100 }, zoom: 1.6 },
      ]),
      FRAME,
      { defaultTransitionMs: 600 },
    );

    let previous = 0;
    for (let t = 2; t <= 2.6001; t += 0.02) {
      const zoom = cropWindowAt(series, t).zoom;
      expect(zoom).toBeGreaterThanOrEqual(previous - 1e-9);
      previous = zoom;
    }
    expect(cropWindowAt(series, 1.99).zoom).toBeCloseTo(1, 6);
    expect(cropWindowAt(series, 2.6).zoom).toBeCloseTo(1.6, 6);
    expect(cropWindowAt(series, 9).zoom).toBeCloseTo(1.6, 6);
  });

  it("holds the previous framing until the cue fires — the zoom never leads", () => {
    const series = resolveCues(
      cueFile([
        { t: 0, rect: null },
        { t: 3, rect: { x: 10, y: 10, width: 100, height: 60 }, zoom: 1.5 },
      ]),
      FRAME,
    );
    for (let t = 0; t < 3; t += 0.05) {
      expect(cropWindowAt(series, t).zoom).toBeCloseTo(1, 9);
    }
  });

  it("clips a transition so consecutive ramps can never overlap", () => {
    const series = resolveCues(
      cueFile([
        { t: 0, rect: null },
        { t: 1.0, rect: null, zoom: 1.4 },
        { t: 1.2, rect: null, zoom: 1.8 },
      ]),
      FRAME,
      { defaultTransitionMs: 600 },
    );
    expect(series.keys[1].transition).toBeCloseTo(0.6, 10);
    expect(series.keys[2].transition).toBeCloseTo(0.2, 10);
  });

  it("honours a per-cue transition override (the Ken Burns push-in)", () => {
    const series = resolveCues(
      cueFile([
        { t: 0, rect: null },
        { t: 4, rect: null, zoom: 1.15, transitionMs: 2500 },
      ]),
      FRAME,
      { defaultTransitionMs: 600 },
    );
    expect(series.keys[1].transition).toBeCloseTo(2.5, 10);
    // Still on its way up 600 ms in — a default-length transition would have
    // finished by now, which is the whole point of the override.
    expect(cropWindowAt(series, 4.6).zoom).toBeLessThan(1.15);
    expect(cropWindowAt(series, 6.5).zoom).toBeCloseTo(1.15, 6);
  });

  it("steps instantly when a cue asks for a zero-length transition", () => {
    const series = resolveCues(
      cueFile([
        { t: 0, rect: null },
        { t: 2, rect: null, zoom: 1.5, transitionMs: 0 },
      ]),
      FRAME,
    );
    expect(cropWindowAt(series, 1.999).zoom).toBeCloseTo(1, 9);
    expect(cropWindowAt(series, 2).zoom).toBeCloseTo(1.5, 9);
  });
});

describe("ffmpeg expression rendering", () => {
  /**
   * Evaluates the subset of the ffmpeg expression language the renderer emits
   * (`+ - * /`, parentheses, `clip()`, `gte()`) so the emitted string can be
   * checked against `evaluateSum` numerically rather than by eyeballing it.
   */
  function evalFfmpegExpr(expr: string, time: number): number {
    const js = expr
      .replaceAll("clip(", "__clip(")
      .replaceAll("gte(", "__gte(")
      .replaceAll("T", String(time));
    const fn = new Function(
      "__clip",
      "__gte",
      `return (${js});`,
    ) as (
      clip: (v: number, lo: number, hi: number) => number,
      gte: (a: number, b: number) => number,
    ) => number;
    return fn(
      (v, lo, hi) => Math.min(Math.max(v, lo), hi),
      (a, b) => (a >= b ? 1 : 0),
    );
  }

  it("renders a sum that evaluates exactly like evaluateSum", () => {
    const series = resolveCues(
      cueFile([
        { t: 0, rect: null },
        { t: 1.5, rect: { x: 100, y: 80, width: 160, height: 40 }, zoom: 1.6 },
        { t: 4.25, rect: { x: 900, y: 600, width: 300, height: 120 }, zoom: 1.35 },
        { t: 6, rect: null, transitionMs: 0 },
      ]),
      FRAME,
    );

    for (const pick of [
      (k: { zoom: number }) => k.zoom,
      (k: { cx: number }) => k.cx,
      (k: { cy: number }) => k.cy,
    ]) {
      const sum = sumSeries(series.keys, pick as never);
      const expr = renderSumExpression(sum, "T");
      for (let t = 0; t <= 8; t += 0.13) {
        expect(evalFfmpegExpr(expr, t)).toBeCloseTo(evaluateSum(sum, t), 3);
      }
    }
  });

  it("maps the crop window onto zoompan's scaled-image coordinates", () => {
    const series = resolveCues(
      cueFile([
        { t: 0, rect: null },
        { t: 2, rect: { x: 400, y: 300, width: 200, height: 100 }, zoom: 1.5 },
      ]),
      FRAME,
    );
    const exprs = buildZoompanExpressions(series, "T");

    for (const t of [0, 1, 2.3, 2.6, 5]) {
      const expected = cropWindowAt(series, t);
      const zoom = evalFfmpegExpr(
        exprs.zoom.replaceAll("max(", "Math.max("),
        t,
      );
      expect(zoom).toBeCloseTo(expected.zoom, 3);

      // zoompan crops `ow×oh` out of an image scaled by `zoom`, so its x/y are
      // the source offset multiplied by the zoom.
      const x = evalFfmpegExpr(
        exprs.x
          .replaceAll("zoom", String(zoom))
          .replaceAll("iw", String(FRAME.width))
          .replaceAll("ow", String(FRAME.width)),
        t,
      );
      const y = evalFfmpegExpr(
        exprs.y
          .replaceAll("zoom", String(zoom))
          .replaceAll("ih", String(FRAME.height))
          .replaceAll("oh", String(FRAME.height)),
        t,
      );
      expect(x / zoom).toBeCloseTo(expected.x, 2);
      expect(y / zoom).toBeCloseTo(expected.y, 2);
    }
  });
});
