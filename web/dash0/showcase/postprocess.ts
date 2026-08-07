/**
 * Showcase post-processing.
 *
 * Playwright emits VP8 `.webm` recordings into `showcase/output/run/`. This
 * script:
 *
 *  1. finds the recording,
 *  2. detects and trims the dead (frozen) frames at its head and tail,
 *  3. re-encodes it to **AV1** with ffmpeg's `libsvtav1` — screen captures
 *     compress extremely well under AV1, keeping the committed asset tiny,
 *  4. copies the finished video and the named still frames into
 *     `web/docs/static/showcase/`, which is what actually gets committed.
 *
 * Raw `.webm` intermediates and everything else under `showcase/output/` stay
 * git-ignored.
 *
 * Run via `make showcase` (which runs the Playwright recording first).
 */
import { spawnSync } from "node:child_process";
import {
  copyFileSync,
  existsSync,
  mkdirSync,
  readdirSync,
  rmSync,
  statSync,
} from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const showcaseDir = path.dirname(fileURLToPath(import.meta.url));
const outputDir = path.join(showcaseDir, "output");
const runDir = path.join(outputDir, "run");
const stillsDir = path.join(outputDir, "stills");
const publishDir = path.resolve(showcaseDir, "../../docs/static/showcase");

/** Stills that the docs Tour page embeds — only these get published. */
const PUBLISHED_STILLS = [
  "01-checks-list.png",
  "02-check-form-filled.png",
  "03-check-detail.png",
];

const PUBLISHED_VIDEO = "create-http-check.mp4";

/** Seconds of context kept around the trimmed region so it doesn't feel abrupt. */
const TRIM_PAD = 0.25;

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
    "AV1 encoder this pipeline uses):",
    "",
    "  macOS         brew install ffmpeg",
    "  Debian/Ubuntu sudo apt-get install -y ffmpeg",
    "  Fedora        sudo dnf install -y ffmpeg",
    "  Arch          sudo pacman -S ffmpeg",
    "",
    "Then re-run `make showcase`.",
  ].join("\n");
}

/** Fails early and clearly if ffmpeg exists but has no AV1 encoder. */
function assertAv1Encoder(): void {
  const { stdout } = run("ffmpeg", ["-hide_banner", "-encoders"], "ffmpeg -encoders");
  if (!stdout.includes("libsvtav1")) {
    fail(
      [
        "Your ffmpeg build has no `libsvtav1` encoder, which the showcase",
        "pipeline needs to produce AV1 video.",
        "",
        "Install an ffmpeg built with SVT-AV1:",
        "",
        "  macOS         brew install ffmpeg          (Homebrew enables libsvtav1)",
        "  Debian/Ubuntu sudo apt-get install -y ffmpeg  (>= 22.04 ships SVT-AV1)",
        "",
        "Then re-run `make showcase`.",
      ].join("\n"),
    );
  }
}

/** Newest `.webm` under the Playwright output directory. */
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
  return found[0];
}

function probeDuration(file: string): number {
  const { stdout } = run(
    "ffprobe",
    [
      "-v",
      "error",
      "-show_entries",
      "format=duration",
      "-of",
      "default=noprint_wrappers=1:nokey=1",
      file,
    ],
    "ffprobe",
  );
  const duration = Number(stdout.trim());
  if (!Number.isFinite(duration) || duration <= 0) {
    fail(`Could not read a duration from ${file} (ffprobe said "${stdout.trim()}")`);
  }
  return duration;
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

/** Turns the detected freezes into a [start, end] window worth keeping. */
function trimWindow(
  freezes: Freeze[],
  duration: number,
): { start: number; end: number } {
  let start = 0;
  let end = duration;

  const leading = freezes.find((f) => f.start <= 0.4);
  if (leading?.end != null && leading.end < duration - 1) {
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
  if (!(end > start + 0.5)) return { start: 0, end: duration };
  return { start, end };
}

function encodeAv1(
  input: string,
  output: string,
  window: { start: number; end: number },
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
      "-c:v",
      "libsvtav1",
      "-crf",
      "34",
      "-preset",
      "6",
      "-g",
      "240",
      "-pix_fmt",
      "yuv420p",
      "-movflags",
      "+faststart",
      output,
    ],
    "AV1 re-encode (libsvtav1)",
  );
}

function humanSize(file: string): string {
  return `${(statSync(file).size / 1024 / 1024).toFixed(2)} MB`;
}

function main(): void {
  assertAv1Encoder();

  const recording = findRecording();
  const duration = probeDuration(recording);
  const window = trimWindow(detectFreezes(recording), duration);

  console.log(`showcase: source     ${path.relative(showcaseDir, recording)} (${duration.toFixed(2)}s)`);
  console.log(
    `showcase: trimmed to ${window.start.toFixed(2)}s → ${window.end.toFixed(2)}s ` +
      `(${(window.end - window.start).toFixed(2)}s kept)`,
  );

  mkdirSync(publishDir, { recursive: true });
  const videoOut = path.join(publishDir, PUBLISHED_VIDEO);
  encodeAv1(recording, videoOut, window);
  console.log(`showcase: wrote      ${path.relative(process.cwd(), videoOut)} (${humanSize(videoOut)}, AV1)`);

  const missing: string[] = [];
  for (const name of PUBLISHED_STILLS) {
    const src = path.join(stillsDir, name);
    if (!existsSync(src)) {
      missing.push(name);
      continue;
    }
    const dst = path.join(publishDir, name);
    copyFileSync(src, dst);
    console.log(`showcase: wrote      ${path.relative(process.cwd(), dst)} (${humanSize(dst)})`);
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
