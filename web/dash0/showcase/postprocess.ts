/**
 * Showcase post-processing.
 *
 * Playwright emits VP8 `.webm` recordings into `showcase/output/run/`, now at
 * 2560×1600 (deviceScaleFactor 2). This script:
 *
 *  1. finds the recording of the create-HTTP-check flow,
 *  2. detects and trims the dead (frozen) frames at its head and tail,
 *  3. reads the cue list the recording wrote alongside it and turns it into a
 *     **camera move** — an ffmpeg `zoompan` that pushes in and back out over
 *     footage that was itself never zoomed,
 *  4. scales the result down to the published 1280×800 and encodes it twice:
 *     **AV1** (`libsvtav1`, tiny) and **H.264** (`libx264`, plays everywhere),
 *  5. downscales the 2× still frames to 1280×800 and copies them, with the
 *     video, into `web/docs/static/showcase/` — which is what gets committed.
 *
 * Raw `.webm` intermediates, cue lists and everything else under
 * `showcase/output/` stay git-ignored.
 *
 * Run via `make showcase` (which runs the Playwright recording first).
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

const showcaseDir = path.dirname(fileURLToPath(import.meta.url));
const outputDir = path.join(showcaseDir, "output");
const runDir = path.join(outputDir, "run");
const stillsDir = path.join(outputDir, "stills");
const cuesDir = path.join(outputDir, "cues");
const publishDir = path.resolve(showcaseDir, "../../docs/static/showcase");

/** Stills that the docs Tour page embeds — only these get published. */
const PUBLISHED_STILLS = [
  "01-checks-list.png",
  "02-check-form-filled.png",
  "03-check-detail.png",
];

/**
 * The two encodes of the same cut.
 *
 * AV1 keeps its original filename because the docs page and the marketing site
 * already point at it. The H.264 twin exists because Safari decodes AV1 only on
 * Apple-silicon machines with the hardware decoder: without a fallback a slice
 * of visitors gets the `<video>` element's error text, and a demo that does not
 * play is worse than no demo.
 */
const PUBLISHED_VIDEO_AV1 = "create-http-check.mp4";
const PUBLISHED_VIDEO_H264 = "create-http-check.h264.mp4";

/** Published frame size. The recording is 2× this; the zoom crops into it. */
const PUBLISHED_WIDTH = 1280;
const PUBLISHED_HEIGHT = 800;

/**
 * Output frame rate. Forcing CFR is what lets the cue timeline be expressed as
 * `on/FPS` inside the filter: Playwright's screencast is variable-rate, so a
 * frame index would otherwise not map to a wall-clock second.
 */
const OUTPUT_FPS = 25;

/**
 * Which take to publish. `make showcase` runs every `*.showcase.ts`, and the
 * SMS opt-in capture records a video too — picking "the newest .webm" would
 * publish that one as the tour video.
 */
const RECORDING_MATCH = "create-http-check";

/** Seconds of context kept around the trimmed region so it doesn't feel abrupt. */
const TRIM_PAD = 0.25;

/** Hand alignment knob for a run whose cue timeline drifted. */
const CUE_OFFSET_S = Number(process.env.SHOWCASE_CUE_OFFSET_MS ?? 0) / 1000;

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

/** Fails early and clearly if ffmpeg exists but lacks an encoder we need. */
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
        "`make showcase` does both steps.",
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

interface TrimWindow {
  start: number;
  end: number;
  /**
   * Source time the cue timeline's `t = 0` corresponds to: the end of the
   * opening frozen frame, which is the moment the first real page painted —
   * exactly when the recording calls `beginCueTimeline()`.
   */
  anchor: number;
}

/** Turns the detected freezes into a [start, end] window worth keeping. */
function trimWindow(freezes: Freeze[], duration: number): TrimWindow {
  let start = 0;
  let end = duration;
  let anchor = 0;

  const leading = freezes.find((f) => f.start <= 0.4);
  if (leading?.end != null && leading.end < duration - 1) {
    anchor = leading.end;
    start = Math.max(0, leading.end - TRIM_PAD);
  }

  const last = freezes[freezes.length - 1];
  if (last && last.start > start + 1) {
    const runsToEnd = last.end == null || last.end >= duration - 0.4;
    if (runsToEnd) {
      end = Math.min(duration, last.start + TRIM_PAD);
    }
  }

  // Never trim into nothing: fall back to the full clip if the maths went odd.
  if (!(end > start + 0.5)) return { start: 0, end: duration, anchor };
  return { start, end, anchor };
}

/**
 * Loads the cue list the recording wrote next to itself, with its times mapped
 * onto the *trimmed* timeline the filter graph will see.
 *
 * Cue times are recorded relative to the first page paint. That paint is what
 * ends the opening freeze, so `window.anchor` is the same instant expressed in
 * source seconds — the one landmark both halves of the pipeline can see.
 */
function loadCues(recording: string, window: TrimWindow): CueFile | null {
  const name = path.basename(path.dirname(recording));
  const file = path.join(cuesDir, `${name}.json`);
  if (!existsSync(file)) return null;

  const parsed = JSON.parse(readFileSync(file, "utf8")) as CueFile;
  const shift = window.anchor + CUE_OFFSET_S - window.start;
  return {
    ...parsed,
    cues: parsed.cues.map((cue) => ({ ...cue, t: cue.t + shift })),
  };
}

/**
 * The filter chain: force CFR, apply the camera move, land on 1280×800.
 *
 * `zoompan` rather than a `crop` with expression-driven `w`/`h`, because a crop
 * whose output size changes per frame forces a filter-link reconfiguration
 * ffmpeg does not handle reliably mid-stream. See `crop-window.ts` for the
 * coordinate mapping.
 */
function buildVideoFilter(series: CueSeries | null, source: VideoInfo): string {
  const parts = [`fps=${OUTPUT_FPS}`];
  if (series && !isIdentitySeries(series)) {
    const exprs = buildZoompanExpressions(series, `on/${OUTPUT_FPS}`);
    parts.push(
      `zoompan=z='${exprs.zoom}':x='${exprs.x}':y='${exprs.y}':d=1:` +
        `s=${source.width}x${source.height}:fps=${OUTPUT_FPS}`,
    );
  }
  parts.push(`scale=${PUBLISHED_WIDTH}:${PUBLISHED_HEIGHT}:flags=lanczos`);
  return parts.join(",");
}

function encode(
  input: string,
  output: string,
  window: TrimWindow,
  filter: string,
  codecArgs: string[],
  what: string,
): void {
  rmSync(output, { force: true });
  run(
    "ffmpeg",
    [
      "-hide_banner",
      "-loglevel",
      "error",
      "-y",
      "-ss",
      window.start.toFixed(3),
      "-to",
      window.end.toFixed(3),
      "-i",
      input,
      "-an",
      "-vf",
      filter,
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

function humanSize(file: string): string {
  const bytes = statSync(file).size;
  return bytes < 1024 * 1024
    ? `${Math.round(bytes / 1024)} KB`
    : `${(bytes / 1024 / 1024).toFixed(2)} MB`;
}

/**
 * Publishes a still at the documented 1280×800.
 *
 * The recording now screenshots at 2×, so the raw stills are 2560×1600. Spec
 * 2026-09-05-03 decided the published catalog stays at 1×; both sizes are
 * printed so the decision can be re-checked against real numbers rather than
 * re-argued.
 */
function publishStill(src: string, dst: string): string {
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

function main(): void {
  assertEncoders();

  const recording = findRecording();
  const source = probeVideo(recording);
  const window = trimWindow(detectFreezes(recording), source.duration);

  console.log(
    `showcase: source     ${path.relative(showcaseDir, recording)} ` +
      `(${source.duration.toFixed(2)}s, ${source.width}×${source.height})`,
  );
  console.log(
    `showcase: trimmed to ${window.start.toFixed(2)}s → ${window.end.toFixed(2)}s ` +
      `(${(window.end - window.start).toFixed(2)}s kept)`,
  );

  const cues = loadCues(recording, window);
  const series = cues
    ? resolveCues(cues, { width: source.width, height: source.height })
    : null;
  if (series) {
    console.log(
      `showcase: cues       ${series.keys.length} points, ` +
        `max zoom ${Math.max(...series.keys.map((k) => k.zoom)).toFixed(2)}× ` +
        `(anchor ${window.anchor.toFixed(2)}s` +
        `${CUE_OFFSET_S ? `, offset ${CUE_OFFSET_S.toFixed(3)}s` : ""})`,
    );
  } else {
    console.log(
      "showcase: cues       none found — publishing the full frame throughout. " +
        "(Expected a file under output/cues/; the recording writes one.)",
    );
  }

  const filter = buildVideoFilter(series, source);

  mkdirSync(publishDir, { recursive: true });

  const av1Out = path.join(publishDir, PUBLISHED_VIDEO_AV1);
  encode(recording, av1Out, window, filter, AV1_ARGS, "AV1 re-encode (libsvtav1)");
  console.log(
    `showcase: wrote      ${path.relative(process.cwd(), av1Out)} (${humanSize(av1Out)}, AV1)`,
  );

  const h264Out = path.join(publishDir, PUBLISHED_VIDEO_H264);
  encode(recording, h264Out, window, filter, H264_ARGS, "H.264 re-encode (libx264)");
  console.log(
    `showcase: wrote      ${path.relative(process.cwd(), h264Out)} (${humanSize(h264Out)}, H.264)`,
  );

  const missing: string[] = [];
  for (const name of PUBLISHED_STILLS) {
    const src = path.join(stillsDir, name);
    if (!existsSync(src)) {
      missing.push(name);
      continue;
    }
    const dst = path.join(publishDir, name);
    console.log(
      `showcase: wrote      ${path.relative(process.cwd(), dst)} (${publishStill(src, dst)})`,
    );
  }
  if (missing.length > 0) {
    fail(
      `The recording did not produce these still frames: ${missing.join(", ")}. ` +
        `Expected them under ${stillsDir} — check the showcase spec's still() calls.`,
    );
  }

  console.log("showcase: done. Commit the assets under web/docs/static/showcase/.");
}

try {
  main();
} catch (err) {
  if (err instanceof PipelineError) {
    console.error(`\nshowcase: ${err.message}\n`);
    process.exit(1);
  }
  throw err;
}
