import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { parseTapeTarget, readTapeCommand, TapeError } from "./tape";

const showcaseDir = path.dirname(fileURLToPath(import.meta.url));

describe("readTapeCommand", () => {
  it("finds the docker run line among the settings", () => {
    const tape = [
      "# a comment",
      "Output output/terminal/frames/",
      'Set Shell "bash"',
      'Type "docker run -p 4000:4000 -v solidping-data:/data ghcr.io/x/y"',
      "Enter",
    ].join("\n");

    expect(readTapeCommand(tape)).toBe(
      "docker run -p 4000:4000 -v solidping-data:/data ghcr.io/x/y",
    );
  });

  it("throws when the command was split off its Type line", () => {
    expect(() => readTapeCommand('Type "docker"\nType " run -p 4000:4000"')).toThrow(
      TapeError,
    );
  });
});

describe("parseTapeTarget", () => {
  it("reads back the port, the volume and the image", () => {
    expect(
      parseTapeTarget(
        "docker run -p 4000:4000 -v solidping-data:/data ghcr.io/fclairamb/solidping",
      ),
    ).toEqual({
      port: 4000,
      volume: "solidping-data",
      image: "ghcr.io/fclairamb/solidping",
    });
  });

  it("refuses a command whose side effects it cannot see", () => {
    // No published port: the guard could not tell whether :4000 is free, and
    // the cleanup could not find what to stop.
    expect(() =>
      parseTapeTarget("docker run -v solidping-data:/data ghcr.io/x/solidping"),
    ).toThrow(TapeError);
    // A bind mount rather than a named volume: nothing for the guard to check.
    expect(() =>
      parseTapeTarget("docker run -p 4000:4000 -v ./data:/data ghcr.io/x/solidping"),
    ).toThrow(TapeError);
  });
});

describe("the committed tape", () => {
  const tape = readFileSync(
    path.join(showcaseDir, "tapes", "docker-run.tape"),
    "utf8",
  );

  it("types the one-liner the docs publish", () => {
    expect(readTapeCommand(tape)).toBe(
      "docker run -p 4000:4000 -v solidping-data:/data ghcr.io/fclairamb/solidping",
    );
  });

  it("is parseable by the guard that has to clean up after it", () => {
    const target = parseTapeTarget(readTapeCommand(tape));

    expect(target.port).toBe(4000);
    expect(target.volume).toBe("solidping-data");
    expect(target.image).toContain("solidping");
  });

  it("renders frames rather than a video", () => {
    // vhs 0.12.0 cannot encode on a modern Go toolchain (it hands an
    // already-cancelled context to the ffmpeg exec), so the pipeline takes the
    // frames and encodes them itself. The trailing slash is what selects that.
    expect(tape).toMatch(/^Output\s+\S+\/\s*$/m);
  });
});
