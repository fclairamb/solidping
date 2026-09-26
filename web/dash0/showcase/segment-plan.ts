/**
 * Cue list → segment plan: the pure half of the showcase *edit*.
 *
 * `crop-window.ts` decides where the camera looks. This decides what the cut is
 * made of: which stretches of the take are played at what speed, when each
 * burned-in lower third is on screen, and how a source timestamp maps onto the
 * finished timeline once a stretch has been sped up.
 *
 * Like `crop-window.ts` it is deliberately free of node imports, so
 * `segment-plan.test.ts` pins its guarantees without ffmpeg, a browser or a
 * recording — see `bun run test:unit`.
 *
 * ## The two kinds of edit, and why they are not the same
 *
 * The detail page has to be held for several full check intervals before the
 * chart plots a line of points genuine intervals apart (see the dwell in the
 * recording spec). Publishing that hold in real time would spend a third of the
 * cut watching a chart not move. So that stretch is played **faster**, with a
 * tag burned in over exactly it — a demo that silently speeds up the boring
 * part is a demo that lies about how fast the product is.
 *
 * Other stretches show nothing at all: a page reloading itself after the
 * password rotation, the pipeline provisioning its org over the API. Speeding
 * those up would only put a tag on screen to announce that nothing is happening
 * faster, so they are **cut** instead — ordinary film grammar, and no claim is
 * made that needs qualifying.
 *
 * Every edit is conditional on the take: when the gap it names turns out to be
 * short (a faster check interval, a quicker machine), the plan reports that it
 * did not bother, with the reason, and that footage plays in real time.
 */

/** A cue, as `postprocess.ts` sees it after shifting onto the trimmed timeline. */
export interface PlanCue {
  /** Seconds into the trimmed take. */
  t: number;
  label?: string;
}

/** One stretch of the source timeline, played at one speed. */
export interface Segment {
  /** Start on the source (trimmed take) timeline, seconds. */
  start: number;
  /** End on the source timeline, seconds. */
  end: number;
  /** Playback speed: 1 is real time, 3 is three times faster. */
  speed: number;
  /** Burned-in tag for a sped-up stretch. Absent whenever `speed` is 1. */
  tag?: string;
}

/**
 * One edit between two named cues.
 *
 * `"speed"` plays the stretch faster and burns a tag over exactly it.
 * `"cut"` removes it outright — the right answer for a stretch that shows
 * nothing at all (a page reloading, the pipeline provisioning an org over the
 * API), where speeding up a static frame would only put a tag on screen to
 * announce that nothing is happening faster.
 */
export type EditSpec =
  | {
      kind: "speed";
      fromLabel: string;
      toLabel: string;
      speed: number;
      /** Below this span the compression is not worth a tag on screen. */
      minSpanS: number;
      /** What to burn in over the sped-up stretch, e.g. "3× speed". */
      tag: string;
    }
  | {
      kind: "cut";
      fromLabel: string;
      toLabel: string;
      /** Below this span the join is not worth the risk of a visible jump. */
      minSpanS: number;
    };

export interface SegmentPlan {
  segments: Segment[];
  /** Length of the source stretch the plan covers. */
  sourceDuration: number;
  /** Length once every segment has been played at its own speed. */
  outputDuration: number;
  /** One line per edit, applied or not, for the run log. */
  notes: string[];
  /** How many edits actually changed the cut. */
  applied: number;
}

function findCue(cues: PlanCue[], label: string): PlanCue | undefined {
  return cues.find((cue) => cue.label === label);
}

function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;

  return Math.min(Math.max(value, min), max);
}

/** An edit resolved against a real take. */
interface ResolvedEdit {
  spec: EditSpec;
  start: number;
  end: number;
}

/**
 * Builds the plan.
 *
 * Every refusal to apply an edit is a *note*, never a throw: a take whose
 * choreography changed should still produce a publishable cut, with the run log
 * saying what it did instead. (Captions are the opposite — a missing caption
 * cue does throw, in `buildLabelWindows`, because it would silently publish a
 * cut that says less than it should.)
 */
export function buildSegmentPlan(input: {
  cues: PlanCue[];
  sourceDuration: number;
  edits?: EditSpec[];
}): SegmentPlan {
  const { cues, sourceDuration, edits = [] } = input;
  const notes: string[] = [];

  if (!(sourceDuration > 0)) {
    return {
      segments: [],
      sourceDuration: 0,
      outputDuration: 0,
      notes: ["the take is empty"],
      applied: 0,
    };
  }

  const resolved: ResolvedEdit[] = [];
  for (const spec of edits) {
    const from = findCue(cues, spec.fromLabel);
    const to = findCue(cues, spec.toLabel);
    if (!from || !to) {
      const missing = [from ? null : spec.fromLabel, to ? null : spec.toLabel]
        .filter(Boolean)
        .join(" and ");
      notes.push(`skipped ${describe(spec)}: the take has no ${missing} cue`);
      continue;
    }

    const start = clamp(from.t, 0, sourceDuration);
    const end = clamp(to.t, 0, sourceDuration);
    const span = end - start;
    if (span < spec.minSpanS) {
      notes.push(
        `skipped ${describe(spec)}: the gap is ${span.toFixed(2)}s, under the ` +
          `${spec.minSpanS.toFixed(2)}s worth editing`,
      );
      continue;
    }
    if (spec.kind === "speed" && !(spec.speed > 1)) {
      notes.push(
        `skipped ${describe(spec)}: a speed of ${spec.speed}× compresses nothing`,
      );
      continue;
    }

    const clash = resolved.find((other) => start < other.end && end > other.start);
    if (clash) {
      notes.push(
        `skipped ${describe(spec)}: it overlaps ${describe(clash.spec)}, and ` +
          "two edits over the same footage cannot both be honoured",
      );
      continue;
    }

    resolved.push({ spec, start, end });
    notes.push(`applied ${describe(spec)} over ${span.toFixed(2)}s`);
  }

  resolved.sort((a, b) => a.start - b.start);

  const segments: Segment[] = [];
  let cursor = 0;
  for (const edit of resolved) {
    if (edit.start > cursor) {
      segments.push({ start: cursor, end: edit.start, speed: 1 });
    }
    if (edit.spec.kind === "speed") {
      segments.push({
        start: edit.start,
        end: edit.end,
        speed: edit.spec.speed,
        tag: edit.spec.tag,
      });
    }
    cursor = edit.end;
  }
  if (cursor < sourceDuration) {
    segments.push({ start: cursor, end: sourceDuration, speed: 1 });
  }

  if (notes.length === 0) notes.push("no edits requested — real time throughout");

  return {
    segments,
    sourceDuration,
    outputDuration: segments.reduce(
      (total, seg) => total + (seg.end - seg.start) / seg.speed,
      0,
    ),
    notes,
    applied: resolved.length,
  };
}

function describe(spec: EditSpec): string {
  const span = `${spec.fromLabel} → ${spec.toLabel}`;

  return spec.kind === "speed" ? `${spec.speed}× over ${span}` : `the cut at ${span}`;
}

/**
 * Where a source timestamp lands on the finished timeline.
 *
 * Needed because every burned-in label is anchored to a *cue* — a moment in the
 * take — while `enable=between(t,…)` in the filter graph counts output seconds,
 * and a compressed stretch makes those two disagree from its start onwards.
 */
export function mapSourceToOutput(plan: SegmentPlan, t: number): number {
  let output = 0;
  for (const segment of plan.segments) {
    if (t <= segment.start) return output;
    const inside = Math.min(t, segment.end) - segment.start;
    output += inside / segment.speed;
    if (t <= segment.end) return output;
  }

  return output;
}

/** A lower third: which cue it belongs to, what it says, how long it stays. */
export interface LabelSpec {
  /** Cue to anchor to, or `null` to sit at the very start of the cut. */
  atCue: string | null;
  text: string;
  holdS: number;
}

/** One burned-in caption, on the finished timeline. */
export interface LabelWindow {
  text: string;
  start: number;
  end: number;
}

/**
 * Turns the label specs into windows on the finished timeline.
 *
 * Two guarantees the published cut depends on, both pinned by the tests: every
 * label appears **exactly once**, and **never two at a time** — a later label
 * truncates the one before it rather than sliding underneath it.
 *
 * A spec whose cue is missing from the take throws. Quietly dropping a caption
 * because a `focus()` label was renamed would publish a cut that silently says
 * less than it is supposed to.
 */
export function buildLabelWindows(input: {
  plan: SegmentPlan;
  cues: PlanCue[];
  labels: LabelSpec[];
  /** Where the take starts on the finished timeline (the terminal segment sits before it). */
  offsetS: number;
  /** Length of the finished cut, to clamp the last window against. */
  outputDuration: number;
}): LabelWindow[] {
  const { plan, cues, labels, offsetS, outputDuration } = input;

  const windows: LabelWindow[] = [];
  for (const spec of labels) {
    let start: number;
    if (spec.atCue === null) {
      start = 0;
    } else {
      const cue = findCue(cues, spec.atCue);
      if (!cue) {
        throw new Error(
          `The lower third "${spec.text}" is anchored to a "${spec.atCue}" cue, ` +
            "which this take does not have (saw: " +
            `${cues.map((c) => c.label ?? "—").join(", ")}). Either the ` +
            "choreography dropped that beat or its focus() label was renamed; " +
            "publishing a cut with one caption silently missing would be worse " +
            "than failing here.",
        );
      }
      start = offsetS + mapSourceToOutput(plan, cue.t);
    }

    start = clamp(start, 0, outputDuration);
    const window = {
      text: spec.text,
      start,
      end: clamp(start + spec.holdS, start, outputDuration),
    };

    const previous = windows[windows.length - 1];
    if (previous && previous.end > window.start) {
      previous.end = Math.max(previous.start, window.start);
    }
    windows.push(window);
  }

  return windows;
}

/** The "3×" tags, as windows on the finished timeline. */
export function buildSpeedTagWindows(
  plan: SegmentPlan,
  offsetS: number,
): LabelWindow[] {
  const windows: LabelWindow[] = [];
  for (const segment of plan.segments) {
    if (!segment.tag) continue;
    windows.push({
      text: segment.tag,
      start: offsetS + mapSourceToOutput(plan, segment.start),
      end: offsetS + mapSourceToOutput(plan, segment.end),
    });
  }

  return windows;
}

/** A window paired with the PNG that was rasterised for it. */
export interface OverlayWindow extends LabelWindow {
  file: string;
  x: string;
  y: string;
}

export interface OverlayPlan {
  /** One `-loop 1 -t <durationS> -i <file>` input per overlay, in order. */
  inputs: { file: string; durationS: number }[];
  /** `filter_complex` fragments; the last one writes `outLabel`. */
  filters: string[];
}

/**
 * Renders the overlay half of the filter graph.
 *
 * The captions are **PNGs composited with `overlay`**, not `drawtext`. drawtext
 * needs an ffmpeg built with libfreetype, and Homebrew's current bottle is not —
 * so a pipeline that reached for it would fail on the one machine that actually
 * regenerates this media. Rasterising the captions in the browser that is
 * already a dependency costs nothing and gives them the product's own
 * typography.
 *
 * Each caption is faded in and out through its alpha channel: `-loop 1 -t D`
 * gives the still its own D-second timeline starting at zero, `fade(alpha=1)`
 * shapes it there, and `setpts` then slides the whole thing to where the
 * `enable` window is.
 */
export function buildOverlayPlan(
  windows: OverlayWindow[],
  opts: {
    firstInput: number;
    baseLabel: string;
    outLabel: string;
    fadeS: number;
    fps: number;
  },
): OverlayPlan {
  const inputs: OverlayPlan["inputs"] = [];
  const filters: string[] = [];

  if (windows.length === 0) {
    return { inputs, filters: [`[${opts.baseLabel}]null[${opts.outLabel}]`] };
  }

  const n = (value: number): string => Number(value.toFixed(3)).toString();

  let previous = opts.baseLabel;
  windows.forEach((window, index) => {
    const duration = Math.max(0, window.end - window.start);
    const fade = Math.min(opts.fadeS, duration / 2);
    const input = opts.firstInput + index;
    const stream = `lbl${index}`;
    const stage =
      index === windows.length - 1 ? opts.outLabel : `ov${index}`;

    inputs.push({ file: window.file, durationS: duration });

    filters.push(
      `[${input}:v]format=rgba,fps=${opts.fps},` +
        `fade=t=in:st=0:d=${n(fade)}:alpha=1,` +
        `fade=t=out:st=${n(duration - fade)}:d=${n(fade)}:alpha=1,` +
        `setpts=PTS+${n(window.start)}/TB[${stream}]`,
    );
    filters.push(
      `[${previous}][${stream}]overlay=${window.x}:${window.y}:` +
        `enable='between(t,${n(window.start)},${n(window.end)})'[${stage}]`,
    );
    previous = stage;
  });

  return { inputs, filters };
}

/**
 * The `trim`/`setpts`/`concat` chain that plays each segment at its own speed.
 *
 * Returns `null` when nothing is compressed, which is the common case and lets
 * the caller skip the split entirely rather than paying for a three-way copy of
 * the stream that re-concatenates into what it already had.
 */
export function buildSpeedFilters(
  plan: SegmentPlan,
  opts: { inLabel: string; outLabel: string; fps: number },
): string[] | null {
  if (plan.segments.every((segment) => segment.speed === 1)) return null;

  const n = (value: number): string => Number(value.toFixed(3)).toString();
  const count = plan.segments.length;
  const parts = plan.segments.map((_, index) => `sp${index}`);

  const filters = [
    `[${opts.inLabel}]split=${count}${parts.map((p) => `[${p}in]`).join("")}`,
  ];

  plan.segments.forEach((segment, index) => {
    const pts =
      segment.speed === 1
        ? "PTS-STARTPTS"
        : `(PTS-STARTPTS)/${n(segment.speed)}`;
    filters.push(
      `[${parts[index]}in]trim=${n(segment.start)}:${n(segment.end)},` +
        `setpts=${pts},fps=${opts.fps}[${parts[index]}]`,
    );
  });

  filters.push(
    `${parts.map((p) => `[${p}]`).join("")}concat=n=${count}:v=1:a=0[${opts.outLabel}]`,
  );

  return filters;
}
