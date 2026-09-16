/**
 * Segment 1 of the showcase cut: the terminal.
 *
 * Renders `tapes/docker-run.tape` with `vhs` — a real `docker run` of the
 * published image, typed out and booted on camera — into
 * `output/terminal/docker-run.mp4`, which `postprocess.ts` then stitches in
 * front of the browser take.
 *
 * ## Why this is a separate step rather than part of postprocess
 *
 * It needs a Docker daemon, a free host port and a network pull; postprocess
 * needs neither. Keeping them apart means a re-cut of an existing take (the
 * common case while tuning the choreography) does not re-run a container.
 *
 * ## Why the tape asks vhs for PNG frames rather than an .mp4
 *
 * vhs 0.12.0 cannot encode on a modern Go toolchain. Its evaluator calls
 * `teardown()` — which cancels the context — and *then* hands that same context
 * to `Render()`, where the ffmpeg command is an `exec.CommandContext`. Since Go
 * 1.20 `Cmd.Start` refuses an already-cancelled context, so ffmpeg never runs:
 * vhs prints "Creating …mp4", logs an empty line where the encoder's output
 * should be, and exits 0 having written nothing. `Output <dir>/` sidesteps it
 * entirely — the frame directory is moved into place from a `defer`, which the
 * cancelled context never reaches.
 *
 * Encoding the frames here is the better arrangement anyway: the segment lands
 * at exactly the pipeline's frame size and rate, and the pipeline stops
 * depending on which ffmpeg options this particular vhs release happens to
 * pass.
 *
 * ## Safety
 *
 * The tape types the *published* one-liner, so what it runs is whatever the
 * docs tell a reader to run — including `-p 4000:4000` and the named volume
 * `solidping-data`. That is the point, and it is also the risk, so this script
 * refuses to start when either is already in use:
 *
 * - the host port must be free (a `make dev` loop on :4000 would otherwise
 *   make the container fail on camera);
 * - the named volume must not already exist (the boot on camera has to be a
 *   first boot, and removing a volume somebody else created is not ours to do).
 *
 * Afterwards it removes exactly what it created: the containers that appeared
 * during the render, and the volume — but only when this run is the one that
 * brought it into existence.
 */
import { spawn, spawnSync } from "node:child_process";
import { createServer } from "node:net";
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
import { parseTapeTarget, readTapeCommand, TapeError } from "./tape";

const showcaseDir = path.dirname(fileURLToPath(import.meta.url));
const tapeFile = path.join(showcaseDir, "tapes", "docker-run.tape");
const terminalDir = path.join(showcaseDir, "output", "terminal");
const framesDir = path.join(terminalDir, "frames");

/** The terminal segment, ready for `postprocess.ts` to stitch in front. */
export const TERMINAL_SEGMENT = path.join(terminalDir, "docker-run.mp4");

/** Published frame size and rate — must match `postprocess.ts`. */
const WIDTH = 1280;
const HEIGHT = 800;
const FPS = 25;

/**
 * The log line that means the server is up. The tape holds past it on a fixed
 * timer (vhs's own `Wait` cannot see scrolled output — see the tape), so this
 * is checked against the container's log afterwards instead of being waited on.
 */
const BOOT_MARKER = "Starting HTTP server";

class TerminalError extends Error {}

function fail(message: string): never {
  throw new TerminalError(message);
}

function which(bin: string): string | null {
  const res = spawnSync("command", ["-v", bin], {
    encoding: "utf8",
    shell: true,
  });

  return res.status === 0 ? res.stdout.trim() : null;
}

/** `vhs` on PATH, or where `go install` puts it. */
function resolveVhs(): string {
  const onPath = which("vhs");
  if (onPath) return "vhs";

  const gopath = spawnSync("go", ["env", "GOPATH"], { encoding: "utf8" });
  if (gopath.status === 0) {
    const candidate = path.join(gopath.stdout.trim(), "bin", "vhs");
    if (existsSync(candidate)) return candidate;
  }

  fail(
    [
      'The terminal segment needs "vhs", which is not on your PATH.',
      "",
      "vhs renders a terminal recording from a text script, which is what keeps",
      "this segment regenerable instead of hand-captured:",
      "",
      "  macOS         brew install vhs",
      "  Any platform  go install github.com/charmbracelet/vhs@latest",
      "                (then put $(go env GOPATH)/bin on your PATH)",
      "",
      "Then re-run `make showcase`.",
    ].join("\n"),
  );
}

function docker(args: string[], what: string): string {
  const res = spawnSync("docker", args, { encoding: "utf8" });
  if (res.error) {
    fail(
      `The terminal segment needs a working Docker daemon (${what} failed to ` +
        `start: ${res.error.message}). Start Docker and re-run \`make showcase\`.`,
    );
  }
  if (res.status !== 0) {
    fail(`${what} failed (exit ${res.status}): ${(res.stderr ?? "").trim()}`);
  }

  return res.stdout ?? "";
}

/** Best-effort docker call for the cleanup path, where a failure must not throw. */
function dockerQuiet(args: string[]): void {
  spawnSync("docker", args, { encoding: "utf8" });
}

async function portIsFree(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const server = createServer();
    server.once("error", () => resolve(false));
    server.once("listening", () => server.close(() => resolve(true)));
    server.listen(port, "127.0.0.1");
  });
}

function containerIds(image: string): Set<string> {
  const out = docker(
    ["ps", "-a", "--filter", `ancestor=${image}`, "--format", "{{.ID}}"],
    "docker ps",
  );

  return new Set(out.split("\n").map((line) => line.trim()).filter(Boolean));
}

function volumeExists(name: string): boolean {
  const out = docker(
    ["volume", "ls", "--filter", `name=^${name}$`, "--format", "{{.Name}}"],
    "docker volume ls",
  );

  return out.split("\n").some((line) => line.trim() === name);
}

/**
 * The platform to request for the container, or `null` when the host runs the
 * image natively.
 *
 * `docker run` prints a two-line WARNING across the top of the frame when the
 * image's architecture differs from the host's *and* no platform was asked for
 * — which, filmed on an Apple-silicon machine against an image that is still
 * published amd64-only, is three seconds of red text in a six-second segment.
 * Naming the platform the image already is silences the warning without
 * changing what runs, and it is a property of the recording machine rather than
 * of the command on camera, so it is set in the environment rather than typed
 * into the tape. On a host that matches the image this is a no-op.
 */
function platformOverride(image: string): string | null {
  const read = (args: string[]): string =>
    (spawnSync("docker", args, { encoding: "utf8" }).stdout ?? "").trim();

  const imageOs = read(["image", "inspect", image, "--format", "{{.Os}}"]);
  const imageArch = read(["image", "inspect", image, "--format", "{{.Architecture}}"]);
  const hostArch = read(["version", "--format", "{{.Server.Arch}}"]);
  const hostOs = read(["version", "--format", "{{.Server.Os}}"]);

  if (!imageOs || !imageArch || !hostArch || !hostOs) return null;
  if (imageOs === hostOs && imageArch === hostArch) return null;

  return `${imageOs}/${imageArch}`;
}

function runVhs(bin: string, platform: string | null): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn(bin, [path.relative(showcaseDir, tapeFile)], {
      cwd: showcaseDir,
      stdio: "inherit",
      env: platform
        ? { ...process.env, DOCKER_DEFAULT_PLATFORM: platform }
        : process.env,
    });
    child.on("error", reject);
    child.on("close", (code) => {
      if (code === 0) resolve();
      else reject(new TerminalError(`vhs exited with ${code}`));
    });
  });
}

function humanSize(file: string): string {
  const bytes = statSync(file).size;

  return bytes < 1024 * 1024
    ? `${Math.round(bytes / 1024)} KB`
    : `${(bytes / 1024 / 1024).toFixed(2)} MB`;
}

function ffmpeg(args: string[], what: string): void {
  const res = spawnSync("ffmpeg", args, {
    encoding: "utf8",
    maxBuffer: 32 * 1024 * 1024,
  });
  if (res.error) {
    fail(`${what} failed to start: ${res.error.message}`);
  }
  if (res.status !== 0) {
    fail(`${what} failed (exit ${res.status}):\n${(res.stderr ?? "").slice(-3000)}`);
  }
}

/**
 * The terminal's background colour, read off the first frame instead of being
 * repeated here as a constant.
 *
 * vhs renders the terminal *inside* the tape's frame — its PNGs are the
 * `Width × Height` box minus twice the padding, grid-rounded — so the encode
 * has to pad back out to the published size, and the padding has to be the
 * theme's background or it shows as a border. Sampling the top-left pixel means
 * changing `Set Theme` in the tape needs no corresponding edit here.
 */
function sampleBackground(frame: string): string {
  const res = spawnSync(
    "ffmpeg",
    ["-v", "error", "-i", frame, "-vf", "crop=1:1:0:0", "-pix_fmt", "rgb24",
      "-f", "rawvideo", "-"],
    { maxBuffer: 1024 },
  );
  const pixel = res.stdout;
  if (res.status !== 0 || !pixel || pixel.length < 3) {
    fail(`Could not sample the terminal background colour from ${frame}.`);
  }

  return `0x${[...pixel.subarray(0, 3)]
    .map((channel) => channel.toString(16).padStart(2, "0"))
    .join("")}`;
}

/**
 * Composites vhs's two PNG sequences — the text layer and the cursor layer —
 * into the segment, at the published size and rate.
 */
function encodeFrames(): number {
  const frames = existsSync(framesDir)
    ? readdirSync(framesDir).filter((f) => f.startsWith("frame-text-"))
    : [];
  if (frames.length === 0) {
    fail(
      `vhs wrote no frames into ${path.relative(showcaseDir, framesDir)}. ` +
        "Check the tape's `Output …/frames/` line (the trailing slash is what " +
        "makes it a frame directory rather than a video file).",
    );
  }

  const background = sampleBackground(path.join(framesDir, "frame-text-00001.png"));

  rmSync(TERMINAL_SEGMENT, { force: true });
  ffmpeg(
    [
      "-hide_banner", "-loglevel", "error", "-y",
      "-r", String(FPS), "-start_number", "1",
      "-i", path.join(framesDir, "frame-text-%05d.png"),
      "-r", String(FPS), "-start_number", "1",
      "-i", path.join(framesDir, "frame-cursor-%05d.png"),
      "-filter_complex",
      `[0][1]overlay[merged];` +
        `[merged]pad=${WIDTH}:${HEIGHT}:(ow-iw)/2:(oh-ih)/2:${background},` +
        `fps=${FPS},setsar=1[out]`,
      "-map", "[out]",
      "-an",
      "-c:v", "libx264", "-crf", "18", "-preset", "veryfast",
      "-pix_fmt", "yuv420p",
      "-movflags", "+faststart",
      TERMINAL_SEGMENT,
    ],
    "terminal segment encode",
  );

  return frames.length / FPS;
}

async function main(): Promise<void> {
  const vhs = resolveVhs();
  const command = readTapeCommand(readFileSync(tapeFile, "utf8"));
  const target = parseTapeTarget(command);

  console.log(`terminal:  tape runs  ${command}`);

  if (!(await portIsFree(target.port))) {
    fail(
      `Port ${target.port} is already in use, and the tape publishes the ` +
        `container on it. The command would fail on camera. Stop whatever is ` +
        `listening there (a \`make dev\` loop, most likely) and re-run.`,
    );
  }

  const volumePreexisted = volumeExists(target.volume);
  if (volumePreexisted) {
    fail(
      `A Docker volume named "${target.volume}" already exists on this machine. ` +
        "The segment has to film a FIRST boot on an empty volume, and removing " +
        "a volume this pipeline did not create is not its call. Remove or " +
        `rename yours (\`docker volume rm ${target.volume}\`) and re-run.`,
    );
  }

  // Pull ahead of the render: a cold pull is minutes of progress bars, and the
  // segment is about how fast the server comes up, not about the download.
  console.log(`terminal:  pulling    ${target.image}`);
  docker(["pull", target.image], "docker pull");

  const platform = platformOverride(target.image);
  if (platform) {
    console.log(
      `terminal:  platform   ${platform} (the image does not match this host, ` +
        "so the run names it explicitly to keep docker's mismatch WARNING off " +
        "camera — the container is the same either way)",
    );
  }

  const before = containerIds(target.image);

  mkdirSync(terminalDir, { recursive: true });
  // vhs *renames* its scratch directory into place, which fails outright when
  // the destination already exists — so a rerun starts from nothing.
  rmSync(framesDir, { recursive: true, force: true });

  let booted = false;
  try {
    await runVhs(vhs, platform);
  } finally {
    for (const id of containerIds(target.image)) {
      if (before.has(id)) continue;
      // Read the log before removing the container: it is the only evidence
      // that the server actually came up while the camera was rolling.
      const logs = spawnSync("docker", ["logs", id], {
        encoding: "utf8",
        maxBuffer: 32 * 1024 * 1024,
      });
      if (`${logs.stdout ?? ""}${logs.stderr ?? ""}`.includes(BOOT_MARKER)) {
        booted = true;
      }
      dockerQuiet(["rm", "-f", id]);
    }
    dockerQuiet(["volume", "rm", target.volume]);
  }

  if (!booted) {
    fail(
      `The container never logged "${BOOT_MARKER}" before the tape cut away, so ` +
        "the segment does not show the server coming up. Lengthen the hold after " +
        "`Enter` in tapes/docker-run.tape and re-run.",
    );
  }

  const duration = encodeFrames();

  console.log(
    `terminal:  wrote      ${path.relative(process.cwd(), TERMINAL_SEGMENT)} ` +
      `(${humanSize(TERMINAL_SEGMENT)}, ${duration.toFixed(2)}s)`,
  );
}

main().catch((err: unknown) => {
  if (err instanceof TerminalError || err instanceof TapeError) {
    console.error(`\nterminal: ${err.message}\n`);
    process.exit(1);
  }
  throw err;
});
