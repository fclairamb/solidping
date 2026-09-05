/**
 * Cue list → crop window: the pure half of the showcase zoom.
 *
 * The recording (`fixtures.ts` → `focus()`) never touches the browser's zoom.
 * It only writes **cue points**: "at t = 4.2 s the interesting thing is this
 * rectangle, frame it at 1.6×". This module turns that list into the framing
 * to apply per moment of the video, and renders the same curve as ffmpeg
 * expressions for `zoompan`.
 *
 * Deliberately free of node/browser imports so it can be unit-tested without a
 * browser — see `crop-window.test.ts`, which is where the clamping, aspect and
 * easing guarantees below are pinned.
 *
 * ## Why the interpolation is a flat sum
 *
 * Every animated value is written as
 *
 *     v(t) = v₀ + Σᵢ Δᵢ · S(clip((t − tᵢ) / Dᵢ, 0, 1))        S(u) = u²(3−2u)
 *
 * `S` is smoothstep: S(0) = 0, S(1) = 1, monotone in between, and C¹ at both
 * ends, so the motion eases in and out with no velocity jump. Because each term
 * saturates at 0 before its cue and at 1 after it — and transitions are clipped
 * so consecutive ones never overlap — the sum reproduces the piecewise
 * "transition, then hold" curve exactly, while staying a *single* expression
 * with no nesting. That matters: the same formula has to survive translation
 * into an ffmpeg expression string, where nested conditionals get unreadable
 * fast.
 *
 * ## Why endpoint clamping is enough
 *
 * A window of zoom `z` can be offset by at most `limit(z) = W(1 − 1/z)` before
 * it leaves the frame. `limit` is concave in `z`, so it lies *above* its own
 * chord; a linearly (in S) interpolated offset between two clamped endpoints is
 * therefore always below `limit` in between. Clamping at the cues is enough to
 * keep every intermediate frame inside the source — no per-frame clamp needed.
 * (`cropWindowAt` clamps anyway, and the emitted ffmpeg expression wraps x/y in
 * `clip()`, because cheap belt-and-braces beats a subtle off-by-one on a
 * deliverable nobody re-checks frame by frame.)
 */

/** A rectangle in CSS pixels, as `Locator.boundingBox()` reports it. */
export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

/** One cue point, exactly as the recording writes it into the cue file. */
export interface FocusCue {
  /** Seconds since the recording anchor (see `beginCueTimeline`). */
  t: number;
  /** What to frame, in CSS px. `null` means "the whole frame". */
  rect: Rect | null;
  /** Magnification. Derived from `rect` when omitted. */
  zoom?: number;
  /** Ease duration into this cue, in ms. Defaults to `defaultTransitionMs`. */
  transitionMs?: number;
  /** Purely informational: how long the recording paused on this cue. */
  holdMs?: number;
  /** Free-text marker, to make a cue file readable. */
  label?: string;
}

/** The on-disk cue file written next to the recording it belongs to. */
export interface CueFile {
  version: 1;
  /** CSS-px viewport the cues were measured in (1280×800). */
  viewport: { width: number; height: number };
  cues: FocusCue[];
}

/** Pixel dimensions of the *source* video (2560×1600 at deviceScaleFactor 2). */
export interface Frame {
  width: number;
  height: number;
}

/** The framing to apply at one instant, in source pixels. */
export interface CropWindow {
  x: number;
  y: number;
  width: number;
  height: number;
  zoom: number;
}

export interface CueOptions {
  /** Hard ceiling on magnification. 2× is pixel-exact at deviceScaleFactor 2. */
  maxZoom?: number;
  /** Ease duration when a cue does not override it. */
  defaultTransitionMs?: number;
  /** Slack kept around a rect when the zoom has to be derived from it. */
  padding?: number;
}

const DEFAULTS = {
  maxZoom: 1.8,
  defaultTransitionMs: 600,
  padding: 1.35,
} as const;

/** One fully-resolved keyframe: centre in source px, plus its ease-in time. */
export interface Keyframe {
  t: number;
  /** Ease duration into this keyframe, in seconds. */
  transition: number;
  zoom: number;
  cx: number;
  cy: number;
  label?: string;
}

/** A resolved cue list plus the frame it was resolved against. */
export interface CueSeries {
  frame: Frame;
  keys: Keyframe[];
}

/** Smoothstep — the ease-in-out curve used for every transition. */
export function smoothstep(u: number): number {
  const c = clamp(u, 0, 1);
  return c * c * (3 - 2 * c);
}

function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;
  return Math.min(Math.max(value, min), max);
}

/**
 * Turns a raw cue file into keyframes in source-pixel space.
 *
 * Each keyframe's window is `frame / zoom` — so it keeps the source aspect
 * ratio by construction — centred on the cue's rectangle and clamped so the
 * window sits inside the frame.
 */
export function resolveCues(
  file: CueFile,
  frame: Frame,
  options: CueOptions = {},
): CueSeries {
  const maxZoom = options.maxZoom ?? DEFAULTS.maxZoom;
  const defaultTransition =
    (options.defaultTransitionMs ?? DEFAULTS.defaultTransitionMs) / 1000;
  const padding = options.padding ?? DEFAULTS.padding;

  const scaleX = frame.width / file.viewport.width;
  const scaleY = frame.height / file.viewport.height;

  const sorted = [...file.cues].sort((a, b) => a.t - b.t);

  const keys: Keyframe[] = sorted.map((cue) => {
    const zoom = clamp(resolveZoom(cue, file.viewport, padding, maxZoom), 1, maxZoom);
    const halfW = frame.width / (2 * zoom);
    const halfH = frame.height / (2 * zoom);

    const rawCx = cue.rect
      ? (cue.rect.x + cue.rect.width / 2) * scaleX
      : frame.width / 2;
    const rawCy = cue.rect
      ? (cue.rect.y + cue.rect.height / 2) * scaleY
      : frame.height / 2;

    return {
      t: cue.t,
      transition:
        cue.transitionMs == null
          ? defaultTransition
          : Math.max(0, cue.transitionMs / 1000),
      zoom,
      cx: clamp(rawCx, halfW, frame.width - halfW),
      cy: clamp(rawCy, halfH, frame.height - halfH),
      label: cue.label,
    };
  });

  // Never let two transitions overlap: the flat-sum interpolation and the
  // "clamped endpoints stay in frame" argument both assume disjoint ramps.
  for (let i = 1; i < keys.length; i++) {
    const gap = keys[i].t - keys[i - 1].t;
    keys[i].transition = Math.max(0, Math.min(keys[i].transition, gap));
  }

  return { frame, keys };
}

function resolveZoom(
  cue: FocusCue,
  viewport: { width: number; height: number },
  padding: number,
  maxZoom: number,
): number {
  if (cue.zoom != null) return cue.zoom;
  if (!cue.rect) return 1;
  if (cue.rect.width <= 0 || cue.rect.height <= 0) return 1;
  return Math.min(
    viewport.width / (cue.rect.width * padding),
    viewport.height / (cue.rect.height * padding),
    maxZoom,
  );
}

/**
 * A single animated scalar, in the flat-sum form documented at the top of the
 * file. `base` is the value before the first cue; each term switches on at `t`
 * and ramps over `dur`.
 */
export interface SumSeries {
  base: number;
  terms: { t: number; dur: number; delta: number }[];
}

/** Extracts one scalar of the keyframe list as a {@link SumSeries}. */
export function sumSeries(
  keys: Keyframe[],
  pick: (key: Keyframe) => number,
): SumSeries {
  if (keys.length === 0) return { base: 0, terms: [] };
  const base = pick(keys[0]);
  const terms: SumSeries["terms"] = [];
  for (let i = 1; i < keys.length; i++) {
    terms.push({
      t: keys[i].t,
      dur: keys[i].transition,
      delta: pick(keys[i]) - pick(keys[i - 1]),
    });
  }
  return { base, terms };
}

/** Evaluates a {@link SumSeries} at time `t` (seconds). */
export function evaluateSum(series: SumSeries, t: number): number {
  let value = series.base;
  for (const term of series.terms) {
    const u = term.dur <= 0 ? (t >= term.t ? 1 : 0) : (t - term.t) / term.dur;
    value += term.delta * smoothstep(u);
  }
  return value;
}

/**
 * The framing to apply at time `t`, in source pixels.
 *
 * With no cues this is the identity window — the whole frame at 1× — which is
 * what makes an un-choreographed recording pass through the pipeline unchanged.
 */
export function cropWindowAt(series: CueSeries, t: number): CropWindow {
  const { frame, keys } = series;
  if (keys.length === 0) {
    return { x: 0, y: 0, width: frame.width, height: frame.height, zoom: 1 };
  }

  const zoom = Math.max(1, evaluateSum(sumSeries(keys, (k) => k.zoom), t));
  const width = frame.width / zoom;
  const height = frame.height / zoom;
  const cx = evaluateSum(sumSeries(keys, (k) => k.cx), t);
  const cy = evaluateSum(sumSeries(keys, (k) => k.cy), t);

  return {
    x: clamp(cx - width / 2, 0, frame.width - width),
    y: clamp(cy - height / 2, 0, frame.height - height),
    width,
    height,
    zoom,
  };
}

function num(value: number): string {
  return Number(value.toFixed(4)).toString();
}

/**
 * Renders a {@link SumSeries} as an ffmpeg expression over `timeExpr`.
 *
 * The output is the literal transcription of {@link evaluateSum}: same base,
 * same terms, same smoothstep. `crop-window.test.ts` evaluates the rendered
 * string and asserts it agrees with `evaluateSum`, so the two can never drift.
 */
export function renderSumExpression(series: SumSeries, timeExpr: string): string {
  let expr = num(series.base);
  for (const term of series.terms) {
    const u =
      term.dur <= 0
        ? `gte(${timeExpr},${num(term.t)})`
        : `clip((${timeExpr}-${num(term.t)})/${num(term.dur)},0,1)`;
    // smoothstep, spelled out: u*u*(3-2*u).
    expr += `+(${num(term.delta)})*(${u})*(${u})*(3-2*(${u}))`;
  }
  return expr;
}

export interface ZoompanExpressions {
  zoom: string;
  x: string;
  y: string;
}

/**
 * Renders the cue series as ffmpeg `zoompan` expressions.
 *
 * `zoompan` scales the input by `z` and crops an `s`-sized rectangle at `(x,y)`
 * out of the scaled image. With `s` equal to the source size, a source window of
 * `frame/zoom` centred on `(cx, cy)` is exactly:
 *
 *     z = zoom          x = cx·zoom − ow/2          y = cy·zoom − oh/2
 *
 * `zoompan` — rather than a `crop` whose `w`/`h` expressions vary — because a
 * crop output size that changes per frame forces a filter-link reconfiguration
 * that ffmpeg does not reliably support mid-stream.
 */
export function buildZoompanExpressions(
  series: CueSeries,
  timeExpr: string,
): ZoompanExpressions {
  if (series.keys.length === 0) {
    return { zoom: "1", x: "0", y: "0" };
  }
  const zoom = `max(1,${renderSumExpression(sumSeries(series.keys, (k) => k.zoom), timeExpr)})`;
  const cx = renderSumExpression(sumSeries(series.keys, (k) => k.cx), timeExpr);
  const cy = renderSumExpression(sumSeries(series.keys, (k) => k.cy), timeExpr);
  return {
    zoom,
    x: `clip((${cx})*zoom-ow/2,0,iw*zoom-ow)`,
    y: `clip((${cy})*zoom-oh/2,0,ih*zoom-oh)`,
  };
}

/** True when the series asks for no movement at all (so the filter can be skipped). */
export function isIdentitySeries(series: CueSeries): boolean {
  return series.keys.every((key) => key.zoom <= 1.0001);
}
