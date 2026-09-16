/**
 * Reading the terminal tape: the pure half of `terminal.ts`.
 *
 * `tapes/docker-run.tape` types the *published* `docker run` one-liner, which
 * means the tape — not this script, and not a second copy of the command in a
 * config object — decides which host port gets bound and which named volume
 * gets created on the machine doing the recording. Those two facts have to be
 * known *before* the render (to refuse to start when either is already in use)
 * and *after* it (to clean up exactly what was created), so they are parsed
 * back out of the tape.
 *
 * Kept free of node imports so `tape.test.ts` can exercise it without a Docker
 * daemon — see `crop-window.ts` for the same split.
 */

/** Raised when a tape cannot be read the way the pipeline needs to read it. */
export class TapeError extends Error {}

/** The `docker run …` line the tape types, verbatim. */
export function readTapeCommand(tape: string): string {
  const match = tape.match(/^[ \t]*Type[ \t]+"(docker run[^"]*)"[ \t]*$/m);
  if (!match) {
    throw new TapeError(
      'The tape has no `Type "docker run …"` line. That line is the single ' +
        "source of truth for the host port and the volume name the pipeline has " +
        "to guard and clean up — keep the command on one Type line.",
    );
  }

  return match[1];
}

export interface TapeTarget {
  /** Host port the container publishes on. */
  port: number;
  /** Named volume mounted into the container. */
  volume: string;
  /** Image reference, exactly as typed (no tag normalisation). */
  image: string;
}

/**
 * What the tape's command will touch on this machine.
 *
 * Deliberately strict: a command this cannot fully understand is a command
 * whose side effects the cleanup path would miss, and a stray container holding
 * :4000 is worse than a failed render.
 */
export function parseTapeTarget(command: string): TapeTarget {
  const port = command.match(/-p[ \t]+(\d+):\d+/);
  const volume = command.match(/-v[ \t]+([A-Za-z0-9][\w.-]*):\//);
  const image = command.match(/[ \t](\S*solidping\S*)[ \t]*$/);

  if (!port) {
    throw new TapeError(
      `Could not read a published host port (-p <host>:<container>) out of: ${command}`,
    );
  }
  if (!volume) {
    throw new TapeError(
      `Could not read a named volume (-v <name>:<path>) out of: ${command}`,
    );
  }
  if (!image) {
    throw new TapeError(`Could not read an image reference out of: ${command}`);
  }

  return { port: Number(port[1]), volume: volume[1], image: image[1] };
}
