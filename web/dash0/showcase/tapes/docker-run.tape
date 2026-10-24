# Segment 1 of the showcase cut: `docker run` to "listening".
#
# Rendered by `web/dash0/showcase/terminal.ts` (which `make showcase` runs
# before the Playwright take) with `vhs`. Nothing here is faked: the command
# typed below is the published one-liner from the README and the Docker
# installation page, and the log that scrolls past is the shipped image booting
# for the first time on an empty volume.
#
# terminal.ts PARSES the `Type` line to learn the host port and the volume name
# it has to guard before the render and clean up after it, so this file is the
# single source of truth for both. Keep the command on one `Type` line, in the
# exact spelling the docs publish.

# PNG frames, not a video, and that is deliberate — see "Why the tape renders
# frames" in showcase/README.md. terminal.ts encodes them itself.
Output output/terminal/frames/

Require docker

Set Shell "bash"
Set Theme "Catppuccin Mocha"
Set FontSize 16
Set Width 1280
Set Height 800
Set Padding 24
Set Framerate 25
Set TypingSpeed 22ms

Sleep 300ms
Type "docker run -p 4000:4000 -v solidping-data:/data ghcr.io/fclairamb/solidping"
Sleep 350ms
Enter

# A fixed hold, NOT `Wait+Screen /SolidPing is ready/`.
#
# vhs 0.12.0's Wait cannot see scrolled output: `Buffer()` reads
# `term.buffer.active.getLine(0..rows)`, which after the first scroll is the top
# of the SCROLLBACK, not the viewport — so it keeps matching against the opening
# prompt until it times out. `Wait+Line` reads one line on the same broken
# offset and behaves the same way.
#
# The native image logs "SolidPing is ready" about a second after Enter. The
# hold ends a few seconds later, before Docker's first HEALTHCHECK probe (~5s
# after start) adds an "HTTP request" line, and there is no Ctrl+C: the shutdown
# log is not part of the story. terminal.ts removes the still-running container
# afterwards, and checks its log first — if that check fires on a slower
# machine, lengthen this Sleep rather than reaching for Wait.
Sleep 3800ms
