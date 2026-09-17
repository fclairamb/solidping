/**
 * Showcase post-processing: the edit.
 *
 * Two sources go in — the `vhs` terminal render from `terminal.ts` and the
 * Playwright take of the dashboard — and one cut comes out, published four
 * ways. The steps:
 *
 *  1. find the recording of the setup-to-first-result flow,
 *  2. detect and trim the dead (frozen) frames at its head and tail,
 *  3. read the cue list the recording wrote alongside it and turn it into a
 *     **camera move** — an ffmpeg `zoompan` that pushes in and back out over
 *     footage that was itself never zoomed,
 *  4. turn the same cue list into a **segment plan** (`segment-plan.ts`):
 *     which stretches play in real time, which — if any — are compressed, and
 *     when each burned-in lower third is on screen,
 *  5. join the terminal segment to the browser one with a short `xfade`, burn
 *     the captions in, and write one **master** at 1280×800,
 *  6. derive everything published from that master: **AV1** (`libsvtav1`,
 *     tiny) and **H.264** (`libx264`, plays everywhere) into
 *     `web/docs/static/showcase/`, plus the README **GIF** and the three
 *     stills into `res/screenshots/` — which is what stops those from being
 *     hand-copied, as they were until spec 2026-09-16-05.
 *
 * Raw `.webm` intermediates, vhs frames, cue lists and everything else under
 * `showcase/output/` stay git-ignored.
 *
 * Run via `make showcase` (which runs the terminal render and the Playwright
 * recording first).
 */
import { spawnSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
} from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  buildZoompanExpressions,
  isIdentitySeries,
  resolveCues,
  type CueFile,
  type CueSeries,
} from "./crop-window";
import {
  buildLabelWindows,
  buildOverlayPlan,
  buildSegmentPlan,
  buildSpeedFilters,
  buildSpeedTagWindows,
  mapSourceToOutput,
  type LabelSpec,
  type LabelWindow,
  type OverlayWindow,
  type EditSpec,
} from "./segment-plan";
import { renderLabelImages } from "./labels";

const showcaseDir = path.dirname(fileURLToPath(import.meta.url));
const outputDir = path.join(showcaseDir, "output");
const runDir = path.join(outputDir, "run");
const stillsDir = path.join(outputDir, "stills");
const cuesDir = path.join(outputDir, "cues");
const labelsDir = path.join(outputDir, "labels");
const masterFile = path.join(outputDir, "master.mp4");
const gifMasterFile = path.join(outputDir, "gif-master.mp4");
const terminalSegment = path.join(outputDir, "terminal", "docker-run.mp4");
const publishDir = path.resolve(showcaseDir, "../../docs/static/showcase");
const screenshotsDir = path.resolve(showcaseDir, "../../../res/screenshots");

/**
 * Stills the docs Tour page embeds, and the name each one takes in
 * `res/screenshots/` — the README's table links those by their own filenames.
 */
const PUBLISHED_STILLS = [
  { still: "01-checks-list.png", readme: "checks-list.png" },
  { still: "02-check-form-filled.png", readme: "check-form.png" },
  { still: "03-check-detail.png", readme: "check-detail.png" },
];

/** The published cut, twice. */
const PUBLISHED_VIDEO_AV1 = "setup-to-first-result.mp4";
const PUBLISHED_VIDEO_H264 = "setup-to-first-result.h264.mp4";

/** What the README embeds, written here rather than copied by hand. */
const README_GIF = "setup-to-first-result.gif";
const README_MP4 = "setup-to-first-result.mp4";

/**
 * Assets the previous cut left behind. Removed on every run: two cuts of the
 * same flow is exactly the rot this pipeline exists to prevent, and the spec
 * that introduced `setup-to-first-result` retired `create-http-check`.
 */
const RETIRED = [
  path.join(publishDir, "create-http-check.mp4"),
  path.join(publishDir, "create-http-check.h264.mp4"),
  path.join(screenshotsDir, "create-http-check.gif"),
  path.join(screenshotsDir, "create-http-check.mp4"),
];

/** Published frame size. */
const PUBLISHED_WIDTH = 1280;
const PUBLISHED_HEIGHT = 800;

/**
 * Output frame rate. Forcing CFR is what lets the cue timeline be expressed as
 * `on/FPS` inside the filter: Playwright's screencast is variable-rate, so a
 * frame index would otherwise not map to a wall-clock second.
 */
const OUTPUT_FPS = 25;

/** How long the terminal segment dissolves into the browser one, in seconds. */
const XFADE_S = 0.4;

/**
 * Which take to publish. `make showcase` runs every `*.showcase.ts`, and the
 * SMS opt-in capture records a video too — picking "the newest .webm" would
 * publish that one as the tour video.
 */
const RECORDING_MATCH = "setup-to-first-result";

/** Seconds of context kept around the trimmed region so it doesn't feel abrupt. */
const TRIM_PAD = 0.25;

/** Hand alignment knob for a run whose cue timeline drifted. */
const CUE_OFFSET_S = Number(process.env.SHOWCASE_CUE_OFFSET_MS ?? 0) / 1000;

/**
 * The edit list: what gets compressed, what gets cut, and what plays straight.
 *
 * Every entry is conditional on the take actually containing that gap, so a
 * faster machine or a faster check interval simply means the footage plays in
 * real time and the run log says so.
 *
 * - **The cuts** remove stretches where nothing is on screen to see: two
 *   request round trips (signing in, saving the check), the app hard-reloading
 *   itself after the rotation, and the seconds the pipeline spends provisioning
 *   its org over the API while the dashboard sits still. None of that is the
 *   product, and a jump cut needs no apology.
 * - **The speed-up** is the dwell on the detail page, which exists so the two
 *   plotted results are a genuine interval apart. That one is *tagged*: it is
 *   product footage played faster, and an untagged speed-up would misrepresent
 *   how fast the check reports. The form's floor is a 10-second interval
 *   (`globalMinPeriodSeconds` in `check-form.tsx`), so the honest dwell is ~12 s
 *   and this fires on every take today; a 5-second interval would leave it
 *   under `minSpanS` and the plan would decline it on its own.
 */
const EDITS: EditSpec[] = [
  {
    kind: "cut",
    fromLabel: "signing-in",
    toLabel: "rotation",
    minSpanS: 1,
  },
  {
    kind: "cut",
    fromLabel: "rotation-done",
    toLabel: "dashboard",
    minSpanS: 1.2,
  },
  {
    kind: "cut",
    fromLabel: "bootstrap",
    toLabel: "checks-list",
    minSpanS: 1.2,
  },
  {
    kind: "cut",
    fromLabel: "saving",
    toLabel: "detail-page",
    minSpanS: 1,
  },
  {
    kind: "speed",
    fromLabel: "detail-page",
    toLabel: "chart",
    speed: Number(process.env.SHOWCASE_TIMELAPSE_SPEED ?? 4),
    minSpanS: Number(process.env.SHOWCASE_TIMELAPSE_MIN_S ?? 9),
    tag: `${Number(process.env.SHOWCASE_TIMELAPSE_SPEED ?? 4)}× speed`,
  },
];

/** Where a lower third sits in the frame. */
const LABEL_X = "56";
const LABEL_Y = "H-h-64";
const LABEL_FADE_S = 0.28;

/**
 * Where a speed tag sits: top right, deliberately nowhere near the lower
 * thirds. The two can overlap in time — the results caption starts exactly
 * where the sped-up stretch does — and stacking them in the same corner drew
 * one caption straight over the other.
 */
const TAG_X = "W-w-56";
const TAG_Y = "56";

/** How long each lower third stays up, in seconds. */
const LABEL_HOLD_S = 3.2;

/** GIF budget and the ladder of compromises that gets under it. */
const GIF_WIDTH = 800;
const GIF_BUDGET_BYTES = 2.5 * 1024 * 1024;
const GIF_ATTEMPTS = [
  { fps: 10, colors: 160, truncate: false },
  { fps: 8, colors: 128, truncate: false },
  { fps: 6, colors: 128, truncate: false },
  { fps: 5, colors: 96, truncate: false },
  { fps: 5, colors: 96, truncate: true },
];

class PipelineError extends Error {}

function fail(message: string): never {
  throw new PipelineError(message);
}

/** Runs a binary, turning a missing executable into an actionable message. */
function run(
  bin: string,
  args: string[],
  what: string,
): { stdout: string; stderr: string } {
  const res = spawnSync(bin, args, { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 });
  if (res.error) {
    const err = res.error as NodeJS.ErrnoException;
    if (err.code === "ENOENT") {
      fail(missingFfmpegMessage(bin));
    }
    fail(`${what} failed to start: ${err.message}`);
  }
  if (res.status !== 0) {
    fail(
      `${what} failed (exit ${res.status}):\n${(res.stderr ?? "").slice(-4000)}`,
    );
  }

  return { stdout: res.stdout ?? "", stderr: res.stderr ?? "" };
}

function missingFfmpegMessage(bin: string): string {
  return [
    `The showcase pipeline needs "${bin}", which is not on your PATH.`,
    "",
    "Install ffmpeg (it ships both ffmpeg and ffprobe, with the libsvtav1",
    "AV1 encoder and the libx264 fallback encoder this pipeline uses):",
    "",
    "  macOS         brew install ffmpeg",
    "  Debian/Ubuntu sudo apt-get install -y ffmpeg",
    "  Fedora        sudo dnf install -y ffmpeg",
    "  Arch          sudo pacman -S ffmpeg",
    "",
    "Then re-run `make showcase`.",
  ].join("\n");
}

/**
 * Fails early and clearly if ffmpeg exists but lacks an encoder we need.
 *
 * Note what is deliberately NOT required: `drawtext`. Homebrew's current bottle
 * is built without libfreetype, so the captions are rasterised in the browser
 * and composited with `overlay` instead — see `labels.ts`.
 */
function assertEncoders(): void {
  const { stdout } = run("ffmpeg", ["-hide_banner", "-encoders"], "ffmpeg -encoders");
  const missing = ["libsvtav1", "libx264"].filter((e) => !stdout.includes(e));
  if (missing.length > 0) {
    fail(
      [
        `Your ffmpeg build has no ${missing.join(" and no ")} encoder.`,
        "",
        "The showcase pipeline publishes the recording twice: AV1 (libsvtav1)",
        "for size, and H.264 (libx264) so Safari without an AV1 hardware",
        "decoder still plays it.",
        "",
        "Install an ffmpeg built with both:",
        "",
        "  macOS         brew install ffmpeg",
        "  Debian/Ubuntu sudo apt-get install -y ffmpeg",
        "",
        "Then re-run `make showcase`.",
      ].join("\n"),
    );
  }
}

/** The recording of the flow we publish, newest take first. */
function findRecording(): string {
  if (!existsSync(runDir)) {
    fail(
      `No Playwright output at ${runDir}. Run the recording first — ` +
        "`make showcase` does every step.",
    );
  }
  const found: string[] = [];
  const walk = (dir: string): void => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) walk(full);
      else if (entry.name.endsWith(".webm")) found.push(full);
    }
  };
  walk(runDir);
  if (found.length === 0) {
    fail(
      `No .webm recording found under ${runDir}. Did the showcase run fail ` +
        "before the browser context closed?",
    );
  }
  found.sort((a, b) => statSync(b).mtimeMs - statSync(a).mtimeMs);

  // Playwright names every recording `video.webm` inside a per-test folder, so
  // the take is identified by that folder.
  const match = found.find((file) =>
    path.basename(path.dirname(file)).includes(RECORDING_MATCH),
  );
  if (!match) {
    fail(
      `No recording of the "${RECORDING_MATCH}" flow found under ${runDir} ` +
        `(saw: ${found.map((f) => path.basename(path.dirname(f))).join(", ")}). ` +
        "Publishing another spec's video under the tour's filename would be worse " +
        "than failing, so this stops here.",
    );
  }

  return match;
}

function requireTerminalSegment(): string {
  if (!existsSync(terminalSegment)) {
    fail(
      `No terminal segment at ${path.relative(showcaseDir, terminalSegment)}. ` +
        "It is the first six seconds of the cut — `docker run` to the server " +
        "coming up — and publishing without it would put the viewer back on an " +
        "already-running dashboard, which is exactly what this cut replaced.\n\n" +
        "Render it with:\n\n  bun run showcase/terminal.ts\n\n" +
        "`make showcase` does it as its first step.",
    );
  }

  return terminalSegment;
}

interface VideoInfo {
  duration: number;
  width: number;
  height: number;
}

function probeVideo(file: string): VideoInfo {
  const { stdout } = run(
    "ffprobe",
    [
      "-v",
      "error",
      "-select_streams",
      "v:0",
      "-show_entries",
      "format=duration:stream=width,height",
      "-of",
      "default=noprint_wrappers=1",
      file,
    ],
    "ffprobe",
  );
  const read = (key: string): number => {
    const match = stdout.match(new RegExp(`^${key}=(.+)$`, "m"));

    return match ? Number(match[1]) : NaN;
  };
  const info = {
    duration: read("duration"),
    width: read("width"),
    height: read("height"),
  };
  if (!Number.isFinite(info.duration) || info.duration <= 0) {
    fail(`Could not read a duration from ${file} (ffprobe said "${stdout.trim()}")`);
  }
  if (!Number.isFinite(info.width) || !Number.isFinite(info.height)) {
    fail(`Could not read the frame size of ${file} (ffprobe said "${stdout.trim()}")`);
  }

  return info;
}

interface Freeze {
  start: number;
  end: number | null; // null = the freeze runs to the end of the file
}

/**
 * Uses ffmpeg's `freezedetect` filter to find static stretches. Playwright
 * recordings routinely open on a blank frame and end on a motionless one; those
 * are the "dead frames" we trim.
 */
function detectFreezes(file: string): Freeze[] {
  // freezedetect writes to stderr and the null muxer produces no output file.
  const res = spawnSync(
    "ffmpeg",
    [
      "-hide_banner",
      "-i",
      file,
      "-vf",
      "freezedetect=n=-60dB:d=0.5",
      "-map",
      "0:v:0",
      "-f",
      "null",
      "-",
    ],
    { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 },
  );
  if (res.error) {
    const err = res.error as NodeJS.ErrnoException;
    if (err.code === "ENOENT") fail(missingFfmpegMessage("ffmpeg"));
    fail(`freeze detection failed to start: ${err.message}`);
  }
  const log = `${res.stderr ?? ""}`;
  const freezes: Freeze[] = [];
  for (const line of log.split("\n")) {
    const start = line.match(/freeze_start:\s*([0-9.]+)/);
    if (start) {
      freezes.push({ start: Number(start[1]), end: null });
      continue;
    }
    const end = line.match(/freeze_end:\s*([0-9.]+)/);
    if (end && freezes.length > 0) {
      freezes[freezes.length - 1].end = Number(end[1]);
    }
  }

  return freezes;
}

/**
 * Finds the sync clapper: the moment `beginCueTimeline()` uncovered the page
 * after briefly blacking it out. Returns the source time of the first frame
 * after the black, or `null` when no clapper is in the footage.
 *
 * This is the one landmark both halves of the pipeline can see. Cue times are
 * wall-clock offsets measured in Node; the video has its own zero that
 * Playwright never discloses. Matching them by guesswork (the opening frozen
 * frame) does not survive a run where the first navigation resolved quickly
 * enough that there was no opening freeze — which is most of them.
 */
function detectClapper(file: string, duration: number): number | null {
  const res = spawnSync(
    "ffmpeg",
    [
      "-hide_banner",
      "-i",
      file,
      "-vf",
      "blackdetect=d=0.12:pic_th=0.99:pix_th=0.06",
      "-map",
      "0:v:0",
      "-f",
      "null",
      "-",
    ],
    { encoding: "utf8", maxBuffer: 64 * 1024 * 1024 },
  );
  if (res.error) return null;

  for (const line of `${res.stderr ?? ""}`.split("\n")) {
    const match = line.match(/black_start:\s*([0-9.]+)\s+black_end:\s*([0-9.]+)/);
    if (!match) continue;
    const end = Number(match[2]);
    // The clapper is at the head of the take, and a black stretch running to
    // the very end of the file is something else entirely.
    if (end < Math.min(duration - 1, 20)) return end;
  }

  return null;
}

interface TrimWindow {
  start: number;
  end: number;
  /**
   * Source time the cue timeline's `t = 0` corresponds to — the clapper's last
   * black frame, i.e. exactly when the recording called `beginCueTimeline()`.
   */
  anchor: number;
  /** How the anchor was established, for the run log. */
  anchorSource: "clapper" | "leading freeze" | "start of file";
}

/** Turns the clapper and the detected freezes into a [start, end] window worth keeping. */
function trimWindow(
  freezes: Freeze[],
  duration: number,
  clapper: number | null,
): TrimWindow {
  let start = 0;
  let end = duration;
  let anchor = 0;
  let anchorSource: TrimWindow["anchorSource"] = "start of file";

  if (clapper != null) {
    // Trim exactly at the clapper: everything before it is the black frame we
    // put there on purpose, plus whatever preceded it.
    anchor = clapper;
    anchorSource = "clapper";
    start = clapper;
  } else {
    const leading = freezes.find((f) => f.start <= 0.4);
    if (leading?.end != null && leading.end < duration - 1) {
      anchor = leading.end;
      anchorSource = "leading freeze";
      start = Math.max(0, leading.end - TRIM_PAD);
    }
  }

  const last = freezes[freezes.length - 1];
  if (last && last.start > start + 1) {
    const runsToEnd = last.end == null || last.end >= duration - 0.4;
    if (runsToEnd) {
      end = Math.min(duration, last.start + TRIM_PAD);
    }
  }

  // Never trim into nothing: fall back to the full clip if the maths went odd.
  if (!(end > start + 0.5)) {
    return { start: 0, end: duration, anchor, anchorSource };
  }

  return { start, end, anchor, anchorSource };
}

/**
 * Loads the cue list the recording wrote next to itself, with its times mapped
 * onto the *trimmed* timeline the filter graph will see.
 */
function readCueFile(recording: string): CueFile | null {
  const name = path.basename(path.dirname(recording));
  let file = path.join(cuesDir, `${name}.json`);
  if (!existsSync(file)) {
    // A take recorded before the cue file was named after the test output
    // directory (or by a hand-run spec) still deserves its choreography.
    const candidates = existsSync(cuesDir)
      ? readdirSync(cuesDir).filter((f) => f.endsWith(".json"))
      : [];
    if (candidates.length !== 1) return null;
    file = path.join(cuesDir, candidates[0]);
    console.log(
      `showcase: cue file  ${candidates[0]} does not match the take's name ` +
        `(${name}.json) but is the only one present — using it.`,
    );
  }

  return JSON.parse(readFileSync(file, "utf8")) as CueFile;
}

/** Default ease duration, mirrored from `crop-window.ts`, for the tail maths. */
const DEFAULT_TRANSITION_S = 0.6;

/** Source time at which the last cue's move has finished playing out. */
function lastCueEnd(file: CueFile, window: TrimWindow): number {
  return file.cues.reduce((latest, cue) => {
    const transition = (cue.transitionMs ?? DEFAULT_TRANSITION_S * 1000) / 1000;

    return Math.max(latest, window.anchor + cue.t + transition);
  }, 0);
}

/**
 * Keeps the tail trim from cutting the final camera move in half.
 *
 * The last cue eases back out to the full frame so the looping `<video>` joins
 * cleanly — but during that ease nothing on the *page* moves, which is exactly
 * what `freezedetect` calls a dead tail. Trimming it away leaves the loop
 * jumping from a zoomed frame to a wide one.
 */
function extendForCues(
  window: TrimWindow,
  file: CueFile,
  duration: number,
): TrimWindow {
  const needed = Math.min(duration, lastCueEnd(file, window) + 0.4);
  if (needed <= window.end) return window;

  return { ...window, end: needed };
}

/** Maps cue times onto the trimmed timeline the filter graph will see. */
function shiftCues(file: CueFile, window: TrimWindow): CueFile {
  const shift = window.anchor + CUE_OFFSET_S - window.start;

  return {
    ...file,
    cues: file.cues.map((cue) => ({ ...cue, t: cue.t + shift })),
  };
}

/**
 * The camera move, as a filter chain over the browser take.
 *
 * `zoompan` rather than a `crop` with expression-driven `w`/`h`, because a crop
 * whose output size changes per frame forces a filter-link reconfiguration
 * ffmpeg does not handle reliably mid-stream. See `crop-window.ts` for the
 * coordinate mapping.
 */
function buildCameraFilter(series: CueSeries | null, source: VideoInfo): string {
  const parts = [`fps=${OUTPUT_FPS}`];
  if (series && !isIdentitySeries(series)) {
    const exprs = buildZoompanExpressions(series, `on/${OUTPUT_FPS}`);
    parts.push(
      `zoompan=z='${exprs.zoom}':x='${exprs.x}':y='${exprs.y}':d=1:` +
        `s=${source.width}x${source.height}:fps=${OUTPUT_FPS}`,
    );
  }
  parts.push(
    `scale=${PUBLISHED_WIDTH}:${PUBLISHED_HEIGHT}:flags=lanczos`,
    "setsar=1",
    "format=yuv420p",
  );

  return parts.join(",");
}

const AV1_ARGS = ["-c:v", "libsvtav1", "-crf", "34", "-preset", "6", "-g", "240"];

/**
 * H.264 tuned for a screen capture: large GOP (almost nothing moves between
 * keyframes), `animation` tuning (flat colour, hard edges — nothing like film
 * grain), and a high enough CRF that the file stays in the same league as the
 * AV1 one.
 */
const H264_ARGS = [
  "-c:v",
  "libx264",
  "-crf",
  "26",
  "-preset",
  "veryslow",
  "-tune",
  "animation",
  "-g",
  "240",
  "-profile:v",
  "high",
];

/** Near-lossless intermediate: everything published is re-encoded from this. */
const MASTER_ARGS = [
  "-c:v",
  "libx264",
  "-crf",
  "14",
  "-preset",
  "veryfast",
  "-tune",
  "animation",
];

function humanSize(file: string): string {
  const bytes = statSync(file).size;

  return bytes < 1024 * 1024
    ? `${Math.round(bytes / 1024)} KB`
    : `${(bytes / 1024 / 1024).toFixed(2)} MB`;
}

/** Re-encodes the master into a published file, no filtering. */
function transcode(
  output: string,
  codecArgs: string[],
  what: string,
  trimTo?: number,
): void {
  rmSync(output, { force: true });
  run(
    "ffmpeg",
    [
      "-hide_banner",
      "-loglevel",
      "error",
      "-y",
      "-i",
      masterFile,
      ...(trimTo ? ["-to", trimTo.toFixed(3)] : []),
      "-an",
      ...codecArgs,
      "-pix_fmt",
      "yuv420p",
      "-movflags",
      "+faststart",
      output,
    ],
    what,
  );
}

/**
 * Publishes a still at the documented 1280×800.
 *
 * The recording screenshots at 2×, so the raw stills are 2560×1600. Spec
 * 2026-09-05-03 decided the published catalog stays at 1×; both sizes are
 * printed so the decision can be re-checked against real numbers rather than
 * re-argued.
 */
function publishStill(src: string, dst: string): string {
  mkdirSync(path.dirname(dst), { recursive: true });
  rmSync(dst, { force: true });
  run(
    "ffmpeg",
    [
      "-hide_banner",
      "-loglevel",
      "error",
      "-y",
      "-i",
      src,
      "-vf",
      `scale=${PUBLISHED_WIDTH}:${PUBLISHED_HEIGHT}:flags=lanczos`,
      dst,
    ],
    `still downscale (${path.basename(src)})`,
  );

  return `${humanSize(dst)} at ${PUBLISHED_WIDTH}×${PUBLISHED_HEIGHT}, ` +
    `${humanSize(src)} at source resolution`;
}

interface GifResult {
  file: string;
  truncatedAtS: number | null;
  fps: number;
  colors: number;
}

/**
 * Writes the README GIF, two-pass, under budget.
 *
 * GitHub renders a GIF inline in a README and will not play a `<video>`, so this
 * is the asset most people actually see. It is also the one that used to be
 * copied in by hand and rot on its own — the point of deriving it here.
 *
 * Fed from the **GIF master** (dashboard only, no camera move), for the reason
 * measured where that master is built. The ladder then trades frame rate, then
 * colours, and only then length: a GIF that stops before the results is worth
 * less than a slightly coarser one, so truncation is the last resort rather
 * than the first. When it happens, the alt text has to say so.
 */
function publishGif(sourceFile: string, truncateAtS: number): GifResult {
  const file = path.join(screenshotsDir, README_GIF);
  const palette = path.join(outputDir, "gif-palette.png");
  mkdirSync(screenshotsDir, { recursive: true });

  let last: GifResult | null = null;
  for (const attempt of GIF_ATTEMPTS) {
    const truncatedAtS = attempt.truncate ? truncateAtS : null;
    const trim = truncatedAtS ? ["-to", truncatedAtS.toFixed(3)] : [];
    const chain = `fps=${attempt.fps},scale=${GIF_WIDTH}:-1:flags=lanczos`;

    rmSync(palette, { force: true });
    run(
      "ffmpeg",
      ["-hide_banner", "-loglevel", "error", "-y", "-i", sourceFile, ...trim,
        "-vf", `${chain},palettegen=max_colors=${attempt.colors}:stats_mode=diff`, palette],
      "GIF palette",
    );

    rmSync(file, { force: true });
    run(
      "ffmpeg",
      ["-hide_banner", "-loglevel", "error", "-y", "-i", sourceFile, ...trim,
        "-i", palette,
        "-lavfi",
        `${chain}[x];[x][1:v]paletteuse=dither=none:diff_mode=rectangle`,
        "-loop", "0", file],
      "GIF encode",
    );

    last = { file, truncatedAtS, fps: attempt.fps, colors: attempt.colors };
    if (statSync(file).size <= GIF_BUDGET_BYTES) {
      rmSync(palette, { force: true });

      return last;
    }
    console.log(
      `showcase: gif        ${humanSize(file)} at ${attempt.fps} fps / ` +
        `${attempt.colors} colours is over the ` +
        `${(GIF_BUDGET_BYTES / 1024 / 1024).toFixed(1)} MB budget — trying harder`,
    );
  }

  rmSync(palette, { force: true });
  console.warn(
    `showcase: WARNING   the README GIF is ${humanSize(file)}, over the ` +
      `${(GIF_BUDGET_BYTES / 1024 / 1024).toFixed(1)} MB budget even at the ` +
      "coarsest setting. Shorten the cut or drop the width.",
  );

  return last as GifResult;
}

/**
 * The captions, in order.
 *
 * The last one is written from what the take actually contains: a cut filmed
 * against a single-node server has no `regions` cue, and claiming "two regions"
 * over footage that shows one picker-less form would be a lie burned into a
 * published asset.
 */
function labelSpecs(hasRegions: boolean): LabelSpec[] {
  return [
    { atCue: null, text: "Run it", holdS: LABEL_HOLD_S },
    { atCue: "login", text: "First login", holdS: LABEL_HOLD_S },
    { atCue: "checks-list", text: "First check", holdS: LABEL_HOLD_S },
    {
      atCue: "detail-page",
      text: hasRegions ? "Results from two regions" : "First results",
      holdS: LABEL_HOLD_S,
    },
  ];
}

async function main(): Promise<void> {
  assertEncoders();

  const terminal = requireTerminalSegment();
  const terminalInfo = probeVideo(terminal);

  const recording = findRecording();
  const source = probeVideo(recording);
  const clapper = detectClapper(recording, source.duration);
  const rawCues = readCueFile(recording);
  let window = trimWindow(detectFreezes(recording), source.duration, clapper);
  if (rawCues) window = extendForCues(window, rawCues, source.duration);

  console.log(
    `showcase: terminal   ${path.relative(showcaseDir, terminal)} ` +
      `(${terminalInfo.duration.toFixed(2)}s, ${terminalInfo.width}×${terminalInfo.height})`,
  );
  console.log(
    `showcase: source     ${path.relative(showcaseDir, recording)} ` +
      `(${source.duration.toFixed(2)}s, ${source.width}×${source.height})`,
  );
  console.log(
    `showcase: trimmed to ${window.start.toFixed(2)}s → ${window.end.toFixed(2)}s ` +
      `(${(window.end - window.start).toFixed(2)}s kept)`,
  );

  const cues = rawCues ? shiftCues(rawCues, window) : null;
  const series = cues
    ? resolveCues(cues, { width: source.width, height: source.height })
    : null;
  if (series) {
    console.log(
      `showcase: cues       ${series.keys.length} points, ` +
        `max zoom ${Math.max(...series.keys.map((k) => k.zoom)).toFixed(2)}× ` +
        `(anchor ${window.anchor.toFixed(2)}s from the ${window.anchorSource}` +
        `${CUE_OFFSET_S ? `, offset ${CUE_OFFSET_S.toFixed(3)}s` : ""})`,
    );
  } else {
    fail(
      `No cue file found for this take (expected one under ${cuesDir}). The ` +
        "burned-in captions are anchored to cues, so a cut cannot be assembled " +
        "without one — check that the recording reached its writeCues() call.",
    );
  }

  if (window.anchorSource !== "clapper") {
    console.warn(
      "showcase: WARNING   no sync clapper found in the footage, so the cue " +
        `timeline was anchored on the ${window.anchorSource} instead. The zooms ` +
        "are probably early or late; check a few frames and nudge with " +
        "SHOWCASE_CUE_OFFSET_MS.",
    );
  }

  // ---- the edit -----------------------------------------------------------

  const planCues = (cues as CueFile).cues.map((cue) => ({
    t: cue.t,
    label: cue.label,
  }));
  const browserDuration = window.end - window.start;
  const plan = buildSegmentPlan({
    cues: planCues,
    sourceDuration: browserDuration,
    edits: EDITS,
  });
  for (const note of plan.notes) {
    console.log(`showcase: edit       ${note}`);
  }

  const offsetS = Math.max(0, terminalInfo.duration - XFADE_S);
  const outputDuration = offsetS + plan.outputDuration;
  const hasRegions = planCues.some((cue) => cue.label === "regions");
  if (!hasRegions) {
    console.warn(
      "showcase: WARNING   this take has no `regions` cue, so it was filmed " +
        "against a server offering a single region and the multi-region beat " +
        "is missing. The results caption says so rather than claiming two. See " +
        "the two-node side-car recipe in showcase/README.md.",
    );
  }

  const lowerThirds: LabelWindow[] = buildLabelWindows({
    plan,
    cues: planCues,
    labels: labelSpecs(hasRegions),
    offsetS,
    outputDuration,
  });
  const speedTags: LabelWindow[] = buildSpeedTagWindows(plan, offsetS);
  const captions = [...lowerThirds, ...speedTags];

  const images = await renderLabelImages(
    captions.map((caption) => caption.text),
    labelsDir,
  );
  const place = (caption: LabelWindow, x: string, y: string): OverlayWindow => {
    const image = images.get(caption.text);
    if (!image) fail(`No caption image was rendered for "${caption.text}".`);

    return { ...caption, file: image.file, x, y };
  };
  const overlayWindows: OverlayWindow[] = [
    ...lowerThirds.map((caption) => place(caption, LABEL_X, LABEL_Y)),
    ...speedTags.map((caption) => place(caption, TAG_X, TAG_Y)),
  ];

  // ---- the masters --------------------------------------------------------
  //
  // Two, because the GIF is a different medium with a different constraint.
  // Measured on this cut at 800 px / 6 fps: the terminal segment costs ~55 KB
  // per GIF frame (a scrolling log changes every pixel of every frame) against
  // ~3 KB for the dashboard, and the camera move nearly doubles the rest for
  // the same reason. Included, they put the README GIF at 6 MB even at 5 fps
  // and 96 colours. So the GIF is rendered from the dashboard take alone, with
  // the camera move left off — and README.md's alt text says that is what it
  // shows. Everything else comes from the full master.

  const speed = buildSpeedFilters(plan, {
    inLabel: "cam",
    outLabel: "browser",
    fps: OUTPUT_FPS,
  });

  /** Assembles one master and returns its measured duration. */
  const assemble = (opts: {
    output: string;
    camera: boolean;
    terminal: boolean;
    captions: OverlayWindow[];
    what: string;
  }): number => {
    // With no terminal segment in front, the browser take starts at zero, so
    // every caption slides back by the offset the join would have added.
    const shift = opts.terminal ? 0 : -offsetS;
    const shifted = opts.captions
      .map((caption) => ({
        ...caption,
        start: caption.start + shift,
        end: caption.end + shift,
      }))
      .filter((caption) => caption.end > 0)
      .map((caption) => ({ ...caption, start: Math.max(0, caption.start) }));

    const firstInput = opts.terminal ? 2 : 1;
    const overlay = buildOverlayPlan(shifted, {
      firstInput,
      baseLabel: opts.terminal ? "joined" : "browsertb",
      outLabel: "cut",
      fadeS: LABEL_FADE_S,
      fps: OUTPUT_FPS,
    });

    // `settb=AVTB` on both sides of the xfade is load-bearing: `trim`/`setpts`
    // leave the browser branch on ffmpeg's microsecond timebase while the
    // terminal branch keeps 1/25 from `fps`, and xfade refuses two inputs whose
    // timebases disagree ("First input link main timebase do not match").
    const graph = [
      `[0:v]${buildCameraFilter(opts.camera ? series : null, source)}[cam]`,
      ...(speed ?? ["[cam]null[browser]"]),
      "[browser]settb=AVTB,setsar=1,format=yuv420p[browsertb]",
      ...(opts.terminal
        ? [
            `[1:v]fps=${OUTPUT_FPS},scale=${PUBLISHED_WIDTH}:${PUBLISHED_HEIGHT}:` +
              "force_original_aspect_ratio=decrease," +
              `pad=${PUBLISHED_WIDTH}:${PUBLISHED_HEIGHT}:(ow-iw)/2:(oh-ih)/2,` +
              "setsar=1,format=yuv420p,settb=AVTB[terminal]",
            `[terminal][browsertb]xfade=transition=fade:duration=${XFADE_S}:` +
              `offset=${offsetS.toFixed(3)}[joined]`,
          ]
        : []),
      ...overlay.filters,
    ].join(";");

    rmSync(opts.output, { force: true });
    run(
      "ffmpeg",
      [
        "-hide_banner", "-loglevel", "error", "-y",
        "-ss", window.start.toFixed(3),
        "-to", window.end.toFixed(3),
        "-i", recording,
        ...(opts.terminal ? ["-i", terminal] : []),
        ...overlay.inputs.flatMap((input) => [
          "-loop", "1",
          "-t", input.durationS.toFixed(3),
          "-i", input.file,
        ]),
        "-filter_complex", graph,
        "-map", "[cut]",
        "-an",
        ...MASTER_ARGS,
        "-pix_fmt", "yuv420p",
        opts.output,
      ],
      opts.what,
    );

    return probeVideo(opts.output).duration;
  };

  mkdirSync(outputDir, { recursive: true });

  const masterDuration = assemble({
    output: masterFile,
    camera: true,
    terminal: true,
    captions: overlayWindows,
    what: "master assembly",
  });
  console.log(
    `showcase: master     ${path.relative(showcaseDir, masterFile)} ` +
      `(${masterDuration.toFixed(2)}s = ${terminalInfo.duration.toFixed(2)}s ` +
      `terminal + ${plan.outputDuration.toFixed(2)}s browser − ` +
      `${XFADE_S.toFixed(2)}s crossfade)`,
  );
  console.log(
    `showcase: captions   ${captions
      .map((c) => `"${c.text}" ${c.start.toFixed(1)}–${c.end.toFixed(1)}s`)
      .join(", ")}`,
  );

  const gifMasterDuration = assemble({
    output: gifMasterFile,
    camera: false,
    terminal: false,
    captions: overlayWindows,
    what: "GIF master assembly",
  });
  console.log(
    `showcase: gif master ${path.relative(showcaseDir, gifMasterFile)} ` +
      `(${gifMasterDuration.toFixed(2)}s, dashboard only, no camera move)`,
  );

  // ---- everything published derives from that master ----------------------

  mkdirSync(publishDir, { recursive: true });

  const av1Out = path.join(publishDir, PUBLISHED_VIDEO_AV1);
  transcode(av1Out, AV1_ARGS, "AV1 re-encode (libsvtav1)");
  console.log(
    `showcase: wrote      ${path.relative(process.cwd(), av1Out)} (${humanSize(av1Out)}, AV1)`,
  );

  const h264Out = path.join(publishDir, PUBLISHED_VIDEO_H264);
  transcode(h264Out, H264_ARGS, "H.264 re-encode (libx264)");
  console.log(
    `showcase: wrote      ${path.relative(process.cwd(), h264Out)} (${humanSize(h264Out)}, H.264)`,
  );

  const readmeMp4 = path.join(screenshotsDir, README_MP4);
  mkdirSync(screenshotsDir, { recursive: true });
  transcode(readmeMp4, H264_ARGS, "README H.264 re-encode");
  console.log(
    `showcase: wrote      ${path.relative(process.cwd(), readmeMp4)} (${humanSize(readmeMp4)}, H.264)`,
  );

  // The GIF's last-resort rung stops where the detail page begins — the same
  // beat, measured on the GIF master's own timeline (which has no terminal
  // segment in front of it, so everything sits `offsetS` earlier).
  const detailCue = planCues.find((cue) => cue.label === "detail-page");
  const gifTruncateAt = detailCue
    ? mapSourceToOutput(plan, detailCue.t)
    : gifMasterDuration;
  const gif = publishGif(gifMasterFile, gifTruncateAt);
  console.log(
    `showcase: wrote      ${path.relative(process.cwd(), gif.file)} ` +
      `(${humanSize(gif.file)}, ${GIF_WIDTH}px, ${gif.fps} fps, ${gif.colors} colours` +
      `${gif.truncatedAtS ? `, TRUNCATED at ${gif.truncatedAtS.toFixed(1)}s` : ""})`,
  );
  if (gif.truncatedAtS) {
    console.warn(
      "showcase: WARNING   the GIF had to stop before the results beat to fit " +
        "its budget. Its alt text in README.md must say so.",
    );
  }

  const missing: string[] = [];
  for (const { still, readme } of PUBLISHED_STILLS) {
    const src = path.join(stillsDir, still);
    if (!existsSync(src)) {
      missing.push(still);
      continue;
    }
    const dst = path.join(publishDir, still);
    console.log(
      `showcase: wrote      ${path.relative(process.cwd(), dst)} (${publishStill(src, dst)})`,
    );
    const readmeDst = path.join(screenshotsDir, readme);
    publishStill(src, readmeDst);
    console.log(
      `showcase: wrote      ${path.relative(process.cwd(), readmeDst)} (${humanSize(readmeDst)})`,
    );
  }
  if (missing.length > 0) {
    fail(
      `The recording did not produce these still frames: ${missing.join(", ")}. ` +
        `Expected them under ${stillsDir} — check the showcase spec's still() calls.`,
    );
  }

  for (const retired of RETIRED) {
    if (existsSync(retired)) {
      rmSync(retired, { force: true });
      console.log(
        `showcase: removed    ${path.relative(process.cwd(), retired)} (retired cut)`,
      );
    }
  }

  console.log(
    "showcase: done. Commit the assets under web/docs/static/showcase/ and " +
      "res/screenshots/.",
  );
}

main().catch((err: unknown) => {
  if (err instanceof PipelineError) {
    console.error(`\nshowcase: ${err.message}\n`);
    process.exit(1);
  }
  throw err;
});
