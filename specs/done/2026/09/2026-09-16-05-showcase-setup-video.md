---
model: opus
effort: high
---

# The showcase video starts on a logged-in dashboard: film the setup itself (docker run to first result), put the multi-region beat on camera, and stop hand-copying the README GIF

## Problem

The only published recording, `web/docs/static/showcase/create-http-check.mp4`
(33.48 s, re-cut by spec `2026-09-05-03`), opens on an already-running,
already-logged-in dashboard. For the audience the app-store listings and the
README are aimed at, the question is "how long until it runs", and nothing on
camera answers it: no `docker run`, no first login, and no forced password
rotation, which is the first screen every new install shows (`CLAUDE.md`,
"Default credentials").

Four more things are wrong with what is published today:

1. **The multi-region beat is missing.** It is the headline difference from
   Uptime Kuma in `wiki/competitors/positioning.md` (buyer table, "distributed
   workers"), the recording spec already has the beat
   (`web/dash0/showcase/specs/create-http-check.showcase.ts:137-153`, gated on
   `regionCount > 0`), and it never fires because the recommended side-car is a
   single node offering one region (`wiki/features/showcase-media.md`, "No
   regions beat").
2. **Stale claims.** `README.md:28` says "18 seconds"; the cut is 33.48 s. The
   wiki already records that a duration must never be hard-coded because it
   moves with every re-cut, and the README still does it.
3. **Hand-copied derivatives.** `res/screenshots/create-http-check.gif` (1.4 MB)
   and `.mp4` were copied by hand in commit `bed9a99d8`; `postprocess.ts`
   writes only to `web/docs/static/showcase/` (`postprocess.ts:46`). The GIF is
   what GitHub actually renders in the README, and it rots independently of
   the pipeline that exists to stop rot.
4. **Length.** A setup segment on top of the current 33 s lands near 50 s. The
   target for a README or a listing is about 30 s.

Not in scope: CI regeneration (still manual by decision of spec
`2026-08-07-02`), and the marketing site (`../solidping-website`), which owes
two fixes recorded in `wiki/features/showcase-media.md` (two `<source>`
children, no duration in the caption) and still ships a single AV1 `src=`
(`src/pages/index.tsx:136-137`). Record that as owed again; do not touch that
repo here.

## Proposal

One new flow, `setup-to-first-result`, produced by the existing pipeline, that
replaces `create-http-check` as the published cut. Four segments, one canvas
(1280×800, 25 fps), burned-in lower-third labels so it reads muted.

### A. Segment 1: the terminal (about 6 s)

A `vhs` tape at `web/dash0/showcase/tapes/docker-run.tape` types the one-liner
from spec `2026-09-15-10` (`docker run -p 4000:4000 -v solidping-data:/data
ghcr.io/fclairamb/solidping`), and holds on the startup log until the
"listening" line. `vhs` renders deterministic terminal video from a script,
which keeps this segment regenerable like everything else; it is a single
binary (`brew install vhs` / `go install`), add it to the prerequisites in
`web/dash0/showcase/README.md` next to ffmpeg. Render at 1280×800 with a dark
theme so the cut to the browser is not a flash-bang. The tape runs the real
image, so the pipeline needs the same disposable-database discipline as the
rest: a throwaway volume name, removed afterwards.

### B. Segment 2: first login and the forced rotation (about 6 s)

A new recording spec `web/dash0/showcase/specs/setup-to-first-result.showcase.ts`
that, against a *fresh* side-car, drives the UI login with the seeded
credentials, lands on `/d/change-password`, sets `ROTATED_PASSWORD`
(`fixtures.ts:74`) and arrives on the empty dashboard. Only then does it call
`apiLogin()` (`fixtures.ts:156`; it already tries the seeded password first
and the rotated one on 401) and `ensureCleanShowcaseOrg()` (`fixtures.ts:271`)
to stage the Northwind org. Ordering matters: today the API bootstrap rotates
the password before any frame is recorded, which is why the rotation has never
been on camera.

### C. Segment 3: create the check, with regions (about 12 s)

Reuse the existing choreography verbatim (the `focus()` cue points, the
painted cursor, character typing). Film it against a two-node side-car so the
region picker renders (`check-form.tsx` shows it when
`availableRegions.length > 1`) and the conditional beat fires with no code
change:

```bash
# Postgres, because two nodes must share one database.
createdb solidping_showcase   # on the dev Postgres from docker-compose.yml, port 55432
PORT=4321 SP_DB_TYPE=postgres SP_DB_URL=postgres://...:55432/solidping_showcase \
  SP_REGION=eu-west ./solidping serve &
SP_NODE_ROLE=checks SP_REGION=us-east SP_DB_TYPE=postgres SP_DB_URL=... ./solidping serve &
```

(`SP_REGION` is read manually at `server/internal/config/config.go:1835-1850`;
`SP_NODE_ROLE` and its accepted values are in `config.go:189-192` /
`node_role.go`.) Put this recipe in the showcase README and the wiki; the
single-node SQLite recipe stays as the fallback and the wiki says which one the
committed cut used.

### D. Segment 4: first results from two regions (about 8 s)

Keep `MIN_DETAIL_DWELL_MS` (`create-http-check.showcase.ts:51`, 11 s, there
for a real interval between two points) but do not publish 11 s of waiting:
`postprocess.ts` applies a 3× `setpts` time-lapse between the `detail-page`
and `chart` cues, with a small "3×" tag burned in for that stretch so it is
honest. If the filmed check can run at a 5-second interval, prefer that and
drop the time-lapse; check the allowed interval values in the check form and
say in the wiki which one was used.

### E. Stitch, label, encode

`postprocess.ts` gains a segment plan: concat the vhs render and the trimmed
browser recording with a 400 ms `xfade`, burn the four lower-third labels
("Run it", "First login", "First check", "Results from two regions") with
`drawtext`, then the existing AV1 + H.264 encodes, published as
`setup-to-first-result.mp4` / `.h264.mp4`. The segment-plan builder (cue
labels in, `[start, end, speed, label]` list out) is a pure function with unit
tests next to `crop-window.test.ts`, so `bun run test:unit` covers it.

### F. Derivatives from the same run

- `postprocess.ts` also writes the README GIF (two-pass `palettegen` /
  `paletteuse`, 800 px wide, 12 fps, target under 2.5 MB) and the three stills
  to `res/screenshots/`, so nothing there is hand-copied any more. If the GIF
  cannot get under the budget at full length, the GIF is segments 1 to 3 only
  and says so in its alt text.
- `README.md:28`: drop the number. "Create an HTTP check, start to finish"
  becomes "From `docker run` to the first result", no duration.
- `web/docs/docs/tour.mdx` embeds the new file; retire `create-http-check.*`
  from `web/docs/static/showcase/` so there is one cut to maintain.
- Update the "Last regenerated" block in `wiki/features/showcase-media.md`
  with the measured length, sizes, the side-car recipe used, and the
  time-lapse or interval decision.

## Verification

- `make showcase` runs end to end on the two-node side-car from a blank
  database and leaves `web/docs/static/showcase/setup-to-first-result.mp4`,
  its H.264 twin, the stills, and `res/screenshots/*.gif` behind.
- The cut is 30 to 38 s; the regions beat is visibly present; the rotation
  screen is on camera; each label appears once.
- `bun run test:unit` green (segment plan + existing crop-window tests);
  `make lint`.
- Play the H.264 file in Safari on a machine without an AV1 decoder.

## Open questions

- `vhs` as a developer dependency: acceptable, or should the terminal segment
  be a Playwright-driven xterm.js page to stay within the existing toolchain?
  Default: `vhs`; it is one binary and the tape is a text file.
- Keep the old `create-http-check` cut for the Tour page as a shorter,
  focused clip? Default: no, one cut.
- Does the `docker run` line in the terminal segment need the `NET_RAW` flag so
  the ICMP story is true later? Default: no, the one-liner is the message.

## Delivery

Branch `feat/showcase-setup-video`, PR title
`feat(showcase): film the setup, first login and multi-region results`.

## Implementation Plan

Execution order. Each numbered step is one commit.

1. **Prerequisites.** Install `vhs` (`brew install vhs`, or
   `go install github.com/charmbracelet/vhs@latest` + `$(go env GOPATH)/bin` on
   `PATH`) and document it next to ffmpeg in `web/dash0/showcase/README.md`.
2. **Segment 1 — terminal.** `web/dash0/showcase/tapes/docker-run.tape` (the
   canonical one-liner from spec `2026-09-15-10`, dark theme, 1280×800) plus
   `web/dash0/showcase/terminal.ts`, which pre-pulls the image, guards the host
   port and the named volume, renders the tape with `vhs`, stops the container
   and removes the volume. Wired into `make showcase` ahead of the Playwright
   step.
3. **Segment plan (pure).** `web/dash0/showcase/segment-plan.ts` +
   `segment-plan.test.ts`: cue labels in, `[start, end, speed, label]` out, the
   source→output time mapping that a sped-up interior span implies, and the
   lower-third label windows derived from it. Covered by `bun run test:unit`.
4. **Login and rotation on camera.** `fixtures.ts` gains `uiFirstLogin()`:
   real form login into the bootstrap org with the seeded password, the forced
   `/d/change-password` screen, the rotation, and the landing on the empty
   dashboard — all before any API bootstrap runs.
5. **The new recording.** `specs/setup-to-first-result.showcase.ts`: segment 2
   (first login + rotation), then `apiLogin()` / `ensureCleanShowcaseOrg()`,
   then segments 3 and 4 reusing the existing choreography verbatim.
   `specs/create-http-check.showcase.ts` is deleted — one cut to maintain.
6. **Stitch, label, encode.** `postprocess.ts`: build one master from the vhs
   render + the trimmed browser take joined by a 400 ms `xfade`, apply the
   segment plan's `setpts` spans, burn the four lower-third labels with
   `drawtext`, then derive AV1, H.264, the README GIF and the stills from that
   single master.
7. **Derivatives and docs.** `res/screenshots/` written by the pipeline instead
   of by hand; `README.md` loses the hard-coded duration; `tour.mdx` embeds the
   new cut; the old `create-http-check.*` assets are deleted.
8. **Recipes and wiki.** The two-node Postgres side-car recipe (the one that
   makes the region picker render) in the showcase README and
   `wiki/features/showcase-media.md`, with the single-node SQLite recipe kept
   as the fallback; "Last regenerated" updated with the measured numbers.
9. **Run it for real**, then QA: `make build-dash0`, dash0 lint,
   `bun run test:unit`, `make build-docs`.
