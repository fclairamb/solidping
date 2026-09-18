# Showcase media pipeline

Produces every published picture of SolidPing: the screen recording on the docs
[Tour page](../../docs/docs/tour.mdx), the video at the top of the root
[`README.md`](../../../README.md), and the three stills both of them embed.

Two sources go in — a `vhs` render of the published `docker run` one-liner
actually booting the shipped image, and a Playwright take driving the **real
dash0 UI** — and one cut comes out, published as AV1, H.264 and three PNGs.

The point is that the media is **regenerable**: when the UI changes, re-run the
pipeline instead of hand-recapturing, so the published assets can never quietly
rot. Nothing under `res/screenshots/` is hand-copied any more — spec
2026-09-16-05 moved that last manual step into `postprocess.ts`.

## This is not a test suite

The showcase project lives outside `web/dash0/e2e/` on purpose:

- the e2e config (`web/dash0/playwright.config.ts`) pins `testDir: "./e2e"`, so
  `bunx playwright test` — and therefore CI — never sees these files;
- the recordings are named `*.showcase.ts`, which does not match Playwright's
  default `testMatch` either.

There is **no CI job and no scheduled regeneration**. It is a manual
`make showcase` only.

The parts of it that *are* covered by CI are the pure ones — `crop-window.ts`
(cue list → crop window), `segment-plan.ts` (cue list → segments, captions and
filter strings) and `tape.ts` (the terminal tape's own guarantees):
`bun run test:unit` picks up `showcase/**/*.test.ts`. Everything else needs a
browser, Docker and ffmpeg, so it is checked by looking at the output.

## The recording org — nothing on camera is a test fixture

These frames get published, so they must not advertise our test rig. The
pipeline therefore **provisions its own organization** rather than filming in
an existing one:

1. it logs in with the bootstrap account (rotating its password first if the
   server insists — see below) and calls `POST /api/v1/orgs` to create the org
   **Northwind Systems** (slug `northwind`) — brand new, so it contains no data
   whatsoever;
2. on a rerun the create returns `409`, so it switches into the existing org
   and **deletes every check in it** before staging anything — a previous run's
   leftovers can never sneak into frame;
3. it sets the account's display name via `PATCH /api/v1/auth/me` so the
   sidebar footer reads as a person (**Alex Rivera**) rather than
   "Administrator";
4. only then does it seed the demo checks and start filming.

This is why the recommended run mode is the **default** one, not
`SP_RUNMODE=test`: test mode's out-of-the-box identity is `test@test.com`, and
its seeded fixtures (e.g. "Notified Check → https://example.com") would show up
in the sidebar and the checks list. Default mode ships
`admin@solidping.io` / `solidpass`, which reads plausibly on camera.

All of this happens inside the pipeline — there is no manual setup step, and
`make showcase` is reproducible from a blank database.

## The forced password rotation, and what it leaves behind

Since spec 2026-08-23-04 a **fresh default-mode database seeds
`admin@solidping.io` with `MustChangePassword = true`**
(`server/internal/jobs/jobtypes/job_startup.go`). The login still succeeds, but
the session it returns reaches only `POST /auth/change-password`,
`GET /auth/me` and `POST /auth/logout`; everything else answers
`403 PASSWORD_CHANGE_REQUIRED` (`server/internal/middleware/auth.go`). Between
2026-08-25 and spec 2026-09-05-03 that silently broke the side-car recipe below:
the run died on its very next call, `POST /api/v1/orgs`.

**Since spec 2026-09-16-05 the rotation is the second beat of the published
cut** — `uiFirstLogin()` drives it through the real form, and the API bootstrap
only runs afterwards. `apiLogin()` still handles the rotation for the SMS
capture and for a rerun, and it is what the notes below describe:

- it detects the flag (on the login response, double-checked against
  `GET /auth/me`) and rotates the password to `SHOWCASE_ROTATED_PASSWORD`;
- **the account stays on that password.** It cannot be rotated back:
  `POST /auth/change-password` refuses a new password equal to the current one
  with `400 VALIDATION_ERROR` ("new password must be different from the current
  one", pinned by `server/internal/handlers/auth/change_password_handler_test.go`).
  That is why the default is `showcase-rotated-pass` and not `solidpass`;
- a rerun against a database this pipeline already rotated works *for
  `apiLogin()`*: the seeded password is tried first, and the rotated one on a
  `401`. **`uiFirstLogin()` deliberately does not do this** — it needs the
  rotation screen to actually appear, so it fails with an actionable error
  instead of quietly filming a plain sign-in. The main take therefore wants a
  genuinely fresh database every time.

Both facts are logged on every run, so nobody has to read this file to find out
why their side-car's admin password changed. If you point the pipeline at a
server you care about, that password change is permanent — one more reason to
use the disposable side-car.

## Prerequisites

- a running SolidPing server (see the side-car recipes below);
- **`ffmpeg`** on `PATH`, built with **both** `libsvtav1` (AV1) and `libx264`
  (the H.264 fallback);
- **`vhs`**, for the terminal segment;
- **Docker**, because that segment runs the published image for real.

```bash
brew install ffmpeg vhs                        # macOS
sudo apt-get install -y ffmpeg                 # Debian/Ubuntu
go install github.com/charmbracelet/vhs@latest # any platform, then put
                                               # $(go env GOPATH)/bin on PATH
```

`drawtext` is deliberately **not** required: Homebrew's current ffmpeg bottle is
built without libfreetype, so the burned-in captions are drawn in the browser
Playwright already ships and composited with `overlay` (`labels.ts`).

## Running it

Against a **disposable side-car server** — the recommended way, for data-safety
reasons as much as to leave a `make dev` loop on :4000 alone (see the warning
below). Note: **default** run mode, no `SP_RUNMODE=test`:

```bash
rm -rf /tmp/showcase-db && mkdir -p /tmp/showcase-db
PORT=4321 SP_DB_TYPE=sqlite SP_DB_DIR=/tmp/showcase-db ./solidping serve &
E2E_BASE_URL=http://localhost:4321/d/ make showcase
```

**This single-node recipe is the fallback, not the one the committed cut uses.**
A single node offers one region, the check form hides its region picker below
two (`availableRegions.length > 1` in `check-form.tsx`), and the multi-region
beat — the headline difference from Uptime Kuma — never reaches the camera. Use
the two-node recipe below unless you only need a quick re-cut.

Note the **`rm -rf`**: the take now films the forced password rotation, which a
fresh default-mode database imposes and an already-used one does not. Recording
against a database this pipeline has run against before fails with an
actionable error rather than quietly filming a plain login.

### The two-node recipe — the one that puts regions on camera

Two nodes sharing one Postgres database, in two regions, both declared through
`SP_REGIONS` (the JSON seed for the `regions` system parameter,
`server/internal/app/regions_seed.go` — a worker's own `SP_NODE_REGION` does not
create a region definition, it only picks one). Postgres because two processes
must share a database, on the dev instance from `docker-compose.yml` (port
`55432`) but in a **throwaway database of its own**:

```bash
# A database this pipeline owns and can drop. NOT the dev `solidping` one.
PGPASSWORD=postgres psql -h localhost -p 55432 -U postgres \
  -c "DROP DATABASE IF EXISTS solidping_showcase" \
  -c "CREATE DATABASE solidping_showcase"

export SP_DB_TYPE=postgres
export SP_DB_URL='postgres://postgres:postgres@localhost:55432/solidping_showcase?sslmode=disable'

# Node A: the API, the jobs, and a check worker in eu-west. It is the one that
# seeds the region definitions, so start it first.
PORT=4321 SP_NODE_NAME=showcase-eu SP_NODE_REGION=eu-west \
  SP_REGIONS='[{"slug":"eu-west","emoji":"🇪🇺","name":"EU West"},{"slug":"us-east","emoji":"🇺🇸","name":"US East"}]' \
  ./solidping serve &

# Node B: a check worker only, in us-east.
PORT=4322 SP_NODE_NAME=showcase-us SP_NODE_ROLE=checks SP_NODE_REGION=us-east \
  ./solidping serve &

curl -s localhost:4321/api/v1/regions   # expect two entries before recording

E2E_BASE_URL=http://localhost:4321/d/ make showcase
```

`SP_NODE_NAME` is not optional here: a worker's identity is derived from the
hostname, and two nodes on one machine would otherwise register as the same
worker. `SP_NODE_REGION` **is** required for the `checks` role
(`ErrRegionRequiredForChecks`), and every worker's region must exist in
`SP_REGIONS` or the node refuses to start.

Afterwards, stop both nodes and drop the database — it is throwaway by design:

```bash
pkill -f "solidping serve"
PGPASSWORD=postgres psql -h localhost -p 55432 -U postgres \
  -c "DROP DATABASE IF EXISTS solidping_showcase"
```

`SP_DB_DIR` is the knob that actually isolates the database — the SQLite file
is written to `$SP_DB_DIR/solidping.db` and `SP_DB_DIR` defaults to `.`, so
**omitting it puts the side-car on the repo-root `./solidping.db`, i.e. your dev
database**. `SP_DB_URL` will not do it: that is the PostgreSQL DSN and is inert
when `SP_DB_TYPE=sqlite`. Deleting the scratch directory between runs is what
gives you a fresh database; `SP_DB_RESET` will not, because it is honored only
in `test`/`demo` run modes (`server/internal/db/sqlite/sqlite.go`, and the same
gate in the Postgres driver) and this recipe deliberately runs in the default
one.

Against whatever is on :4000:

```bash
make showcase
```

Two narrower entry points exist for iterating: `make showcase-terminal`
re-films only the `docker run` segment, and `make showcase-cut` re-runs the edit
over takes already in `showcase/output/` without recording anything.

> ### ⚠️ What a run leaves behind in the target database
>
> The pipeline **writes to whatever server you point it at**, and two of those
> writes are permanent:
>
> - **The `northwind` / "Northwind Systems" organization persists.** It is
>   created on the first run and never deleted — there is no delete-org
>   endpoint, and reruns deliberately reuse it. Point the pipeline at your dev
>   database and that org is in your org switcher from then on.
> - **The bootstrap account's password may be rotated**, permanently, to
>   `SHOWCASE_ROTATED_PASSWORD` — see the section above.
> - Its checks do *not* persist: the org is emptied at the end of every run and
>   wiped clean again at the start of the next one.
> - The bootstrap account's **display name is borrowed, not kept**: it is read
>   before the recording, set to `SHOWCASE_USER_NAME` for the duration, and
>   restored in the recording's `finally` block. A completed run leaves the
>   user record exactly as it found it. (`PATCH /api/v1/auth/me` writes the
>   *global* user row — `OrganizationMember` has no per-org display name — so
>   without that restore a run would rename the account everywhere. If a run is
>   hard-killed mid-recording, the restore never happens; check the account's
>   name before assuming it did.)
>
> Use the disposable side-car above and none of this touches anything you care
> about.

Useful knobs:

| Env var | Default | Meaning |
|---|---|---|
| `E2E_BASE_URL` | `http://localhost:4000/d/` | Server to record against (same convention as the e2e suite) |
| `SHOWCASE_BOOTSTRAP_ORG` / `SHOWCASE_EMAIL` / `SHOWCASE_PASSWORD` | `default` / `admin@solidping.io` / `solidpass` | Account used to bootstrap; also the identity that appears on camera |
| `SHOWCASE_ROTATED_PASSWORD` | `showcase-rotated-pass` | Password the account is rotated onto when the server forces a rotation, and **stays on**. Must differ from `SHOWCASE_PASSWORD` and be ≥ 8 characters |
| `SHOWCASE_ORG` / `SHOWCASE_ORG_NAME` | `northwind` / `Northwind Systems` | The org that gets provisioned and filmed |
| `SHOWCASE_USER_NAME` | `Alex Rivera` | Display name shown in the sidebar footer |
| `SHOWCASE_CURSOR` | on | Set to `0` to record without the synthetic pointer |
| `SHOWCASE_TRAVEL_MS` | `420` | How long the cursor takes to travel to a control before clicking it |
| `SHOWCASE_SLOW_MO` | `0` | Playwright `slowMo`. Escape hatch only — it delays *every* input step, including each step of the eased cursor travel |
| `SHOWCASE_CLAPPER_MS` | `320` | How long the black sync clapper covers the frame |
| `SHOWCASE_CUE_OFFSET_MS` | `0` | Nudge the whole zoom timeline earlier/later if a run drifted |
| `SHOWCASE_DOCKER_PULL` | `1` | `0` films the image already tagged `ghcr.io/fclairamb/solidping` locally instead of pulling — see "The terminal segment" below |
| `SHOWCASE_TIMELAPSE_SPEED` | `4` | How fast the dwell on the detail page is played, and what the burned-in tag says |
| `SHOWCASE_TIMELAPSE_MIN_S` | `9` | Below this, the dwell is published in real time and the tag never appears |

## What it does

1. **`terminal.ts` films the terminal** (`make showcase-terminal`). It renders
   `tapes/docker-run.tape` with `vhs`: the published one-liner typed out, the
   shipped image booting on an empty volume, held past its "Starting HTTP
   server" line. Everything on camera is real.

   It refuses to start when the host port the tape publishes is already in use
   or when the named volume already exists — the first would make the command
   fail on camera, the second would film a second boot rather than a first —
   and it removes exactly what it created afterwards: the container that
   appeared during the render, and the volume.

   > **Why the tape asks vhs for PNG frames rather than an .mp4.** vhs 0.12.0
   > cannot encode on a modern Go toolchain: its evaluator calls `teardown()`,
   > which cancels the context, and then hands that same context to `Render()`,
   > where the ffmpeg command is an `exec.CommandContext`. Since Go 1.20
   > `Cmd.Start` refuses an already-cancelled context, so ffmpeg never runs —
   > vhs prints "Creating …mp4", logs an empty line where the encoder output
   > should be, and exits 0 having written nothing. `Output <dir>/` sidesteps
   > it: the frame directory is moved into place from a `defer`. `terminal.ts`
   > then composites vhs's text and cursor layers itself, which also pins the
   > segment to the pipeline's own frame size and rate.

   > **Why the hold is a fixed `Sleep` and not `Wait+Screen`.** vhs's `Wait`
   > cannot see scrolled output: `Buffer()` reads
   > `term.buffer.active.getLine(0..rows)`, which after the first scroll is the
   > top of the *scrollback*, not the viewport, so it matches against the
   > opening prompt until it times out. The tape holds on a timer instead, and
   > `terminal.ts` checks the container's own log afterwards — a render where
   > the server never reached "Starting HTTP server" fails rather than
   > publishing a segment that does not show the thing it promises.

   > **`SHOWCASE_DOCKER_PULL=0`.** `:latest` is published amd64-only until the
   > arm64 build of spec 2026-09-15-09 reaches a release. Filmed under emulation
   > on an Apple-silicon machine it boots slowly enough to miss the hold and
   > puts an emulation-only "slow SQL query" wall of DDL on camera, so the
   > committed cut was filmed against an image built from the working tree
   > (`docker build -t ghcr.io/fclairamb/solidping .`) with this variable set.
   > That overwrites the local tag — `docker pull ghcr.io/fclairamb/solidping`
   > puts the published image back.

2. **`specs/setup-to-first-result.showcase.ts` films the dashboard.** It signs
   in through the real form with the seeded credentials, goes through the
   **forced password rotation** a fresh install imposes, lands on the dashboard,
   and only *then* bootstraps over the API (that ordering is the point — the
   rotation used to be satisfied before any frame was recorded, which is why it
   had never been on camera). Then: checks list → **New check** → name + target
   URL → interval → regions → save → check detail page, with named still frames
   written along the way. The org is emptied afterwards.

   > **There is deliberately no check-type step.** The form defaults to HTTP
   > (`initialType = initialData?.type || "http"`, `check-form.tsx`), so
   > opening the combobox to choose the value already selected filmed "HTTP" →
   > "HTTP" — dead time. The spec asserts the default instead, so a change to
   > it fails the run rather than quietly filming the wrong type.

   > **The regions beat only fires against a server that offers more than one
   > region.** The form renders the region picker only when
   > `availableRegions.length > 1`
   > (`web/dash0/src/components/shared/check-form.tsx`), and the spec gates the
   > whole beat — cue included — on `regionCount > 0`. A single-node side-car
   > offers one region and silently records no regions step; `postprocess.ts`
   > notices, warns, and writes "First results" instead of "Results from two
   > regions" so the caption cannot claim something the footage does not show.
   > Use the two-node recipe above.

3. **`postprocess.ts` cuts it together** (`make showcase-cut`): trims the take
   at the clapper, applies the camera move, applies the **segment plan**
   (below), joins the terminal segment to the dashboard one with a 400 ms
   `xfade`, burns in the four lower thirds, and writes one master — from which
   the AV1, the H.264 and the three stills are all derived.

### The edit: what is cut, what is sped up, and what is neither

The cut is assembled from a **segment plan** (`segment-plan.ts`, pure and
unit-tested) built from the recording's own cue labels:

- **Cuts.** Stretches where nothing is on screen — the app hard-reloading
  itself after the rotation, the seconds the pipeline spends provisioning its
  org over the API, a slow request round trip — are removed outright. Ordinary
  film grammar; no claim is made that needs qualifying.
- **One speed-up, always tagged.** The dwell on the detail page exists so the
  chart plots two results a genuine interval apart; the check form's floor is a
  10-second interval (`globalMinPeriodSeconds` in `check-form.tsx`), so that
  dwell is around twelve seconds. It is played at `SHOWCASE_TIMELAPSE_SPEED`
  with the speed burned into the top-right corner **for exactly that stretch**.
  A demo that quietly speeds up its slow part lies about how fast the product
  is; one that says "4× speed" does not.
- **Everything else is real time.** Every edit is conditional on the gap it
  names actually being long enough, so a faster machine or a faster interval
  simply publishes the footage as filmed — the run log prints one line per
  edit, applied or skipped, with the reason.

### The four lower thirds

"Run it", "First login", "First check" and "Results from two regions" are PNGs
rasterised in Chromium (`labels.ts`) and composited with `overlay`, each faded
in and out through its alpha channel. Two guarantees are pinned by the unit
tests: every caption appears **exactly once**, and never two at a time. A
caption whose cue is missing from the take is a hard failure — publishing a cut
that silently says less than it should is worse than failing the run.

### Refreshing the README video

**This is the one step `make showcase` cannot do for you.** Everything else in
this pipeline is regenerable; the README's player is not, and it needs three
manual minutes after a re-record.

The root README used to embed a GIF, because a GIF is the only moving format
GitHub renders inline from a path in the repo. It cost 2.35 MB in every clone
and it could not show the two most expensive beats: measured at 800 px / 6 fps,
the terminal segment costs about **55 KB per GIF frame** — a scrolling log
changes every pixel of every frame, which is the one thing GIF cannot compress
— against about 3 KB for the dashboard, and the camera move nearly doubles the
rest for the same reason. The full cut was 6 MB at 5 fps and 96 colours, so the
GIF was rendered from a second, stripped master with neither.

GitHub does play a real `<video>`, but only when the source is an attachment on
its own CDN. So the README now embeds the H.264 cut, uploaded once and linked by
its `user-attachments` URL. H.264 does not care about scrolling logs or camera
moves, so the README and the Tour page finally show the same 38 seconds.

To refresh it after `make showcase`:

1. Open a GitHub issue or issue comment in this repo and drag
   `web/docs/static/showcase/setup-to-first-result.h264.mp4` into the body.
   Only `.mp4`, `.mov` and `.webm` are accepted, H.264 is the safest codec, and
   the cap is 10 MB on a free plan.
2. Wait for the upload to finish. The body is replaced by a
   `https://github.com/user-attachments/assets/<uuid>` URL.
3. **Post the comment.** An upload that is never posted stays private to the
   uploader: the URL 404s for everyone else, and the README player is broken for
   every visitor but you. [Issue #396](https://github.com/fclairamb/solidping/issues/396)
   exists to hold these — post there and leave it closed.
4. Paste the URL into `README.md`, on **its own line with a blank line either
   side**. Inside the centred `<div>` is fine; GitHub renders it as a player
   there. Do not wrap it in `<video>`, markdown link syntax, or `<img>` — any of
   those and the sanitizer or the renderer gives you a bare link instead.

The old URL keeps working, so nothing breaks between the re-record and the
paste. The trade-offs this buys: the video lives on GitHub's CDN rather than in
the repo, so it is absent from a clone, a fork, a tarball, and from any renderer
that is not github.com (Docker Hub, pkg.go.dev, an IDE preview). Those see the
bare URL. The `<sub>` caption under the player carries the description that used
to be the GIF's alt text, and points at the Tour page as the fallback.

### Making it look like a demo rather than a screen capture

Three things, none of which touch the UI being filmed:

- **A synthetic cursor.** Headless Chromium composites no pointer into its
  screencast, so `installCursor()` injects one (`context.addInitScript`): a
  `pointer-events: none` SVG arrow that follows `mousemove`, with a click
  ripple. It is hidden while `still()` takes a screenshot — published stills are
  pictures of the product, not of the rig — and the **SMS opt-in capture never
  installs it at all** (`SHOWCASE_CURSOR=0` does the same globally). Those
  stills are evidence for a carrier reviewer and must show only shipped pixels.
- **Human input.** `moveTo()` walks the pointer to a control in eased,
  real-time steps before clicking; `typeHuman()` types character by character
  at 40–70 ms. Playwright's own `mouse.move(..., { steps })` emits the whole
  path in one burst, which the screencast never sees, and `fill()` makes a URL
  appear in a single frame.
- **A camera move, added in post.** `focus(page, locator, { zoom })` records a
  *cue point* — what mattered, and when — into `output/cues/`. The browser is
  never zoomed. `postprocess.ts` turns the cue list into an ffmpeg `zoompan`
  that eases between framings with a smoothstep curve. That is why the motion is
  smooth no matter how jerkily Playwright drove the UI, and why re-timing the
  choreography costs nothing.

### How the two timelines are aligned — the clapper

Cue times are wall-clock offsets measured in Node; the video has a zero of its
own that Playwright never discloses. So the recording **claps**: `uiLogin()`
covers the page with opaque black for ~320 ms, and `t = 0` is the instant it is
uncovered. `postprocess.ts` finds that same instant with ffmpeg's `blackdetect`,
uses it as the anchor, and trims everything before it — the clapper never
reaches the published cut. (An earlier design anchored on the opening *frozen*
frame instead; that fails whenever the first navigation resolved fast enough
that there was no opening freeze, which is most runs. If the clapper is ever
missing, postprocess falls back to the old behaviour and says so loudly.)

The generator itself — cue list in, per-instant crop window out — is a pure
function in `crop-window.ts` with unit tests for clamping at the frame edges,
aspect preservation, easing monotonicity, the "no cues → identity crop" case,
and an evaluation cross-check between the emitted ffmpeg expression and the
TypeScript one.

### Resolution: what 2× buys and what it does not

`deviceScaleFactor: 2` gives genuinely 2× **stills** — `page.screenshot()`
renders at the device scale, so the raw PNGs are 2560×1600.

It does **not** give a 2× **video**. Playwright records Chromium by encoding CDP
screencast frames, and those come back at the CSS-pixel size of the viewport
regardless of the device scale factor. Measured here: with
`deviceScaleFactor: 2` the screenshots are 2560×1600 while the screencast frames
stay 1280×800, and asking for a 2560×1600 video does not upscale them —
Playwright pastes each 1280×800 frame into the top-left corner of the requested
canvas and leaves the rest flat grey. (Re-issuing
`Emulation.setDeviceMetricsOverride` with `scale: 2` over a raw CDP session does
not change it either.) So `video.size` **must** equal the viewport in CSS px.

The consequence is real: the post-production zoom is an *upscale* of 1× pixels.
That is why the choreography stays at or below 1.6× and the scale-down uses
`lanczos`. Getting truly crisp zooms would mean recording a 2560×1600 CSS
viewport with the app scaled up — i.e. filming a layout nobody ships — which is
deliberately not done.

Published stills stay at **1280×800**. The 2560×1600 PNGs from the last run
measure 259 / 239 / 282 KB — two of the three above the ~250 KB-each bar that
would have justified publishing retina stills — while the 1× versions actually
committed are 177 / 166 / 198 KB. The committed catalog staying small is the
constraint that decides it. (KB here is KiB, the unit `postprocess.ts` prints,
so these match the run log line for line.)

Frame interpolation (`minterpolate` to 50 fps) was tried and **rejected**: on
screen content it ghosts, doubling half-typed characters and the text caret on
every synthesised frame. The recording stays at its native 25 fps.

## Files

| Path | Committed? | What |
|---|---|---|
| `showcase/output/` | **no** (git-ignored) | Raw `.webm`, vhs frames, cue lists, caption PNGs, both masters, all pipeline scratch |
| `web/docs/static/showcase/setup-to-first-result.mp4` | yes | The cut, AV1 |
| `web/docs/static/showcase/setup-to-first-result.h264.mp4` | yes | The same cut in H.264, for browsers without AV1 |
| `web/docs/static/showcase/0*.png` | yes | Stills embedded in the Tour page |
| `github.com/user-attachments/assets/…` | **not in the repo** | What GitHub plays at the top of the root README — uploaded by hand, see "Refreshing the README video" |
| `res/screenshots/check*.png`, `checks-list.png` | yes | The root README's screenshot table |

Everything in both directories is written by `postprocess.ts`. Nothing there is
copied by hand — that was the state until spec 2026-09-16-05, and it is exactly
how a committed asset rots. Keep the catalog small all the same: only assets a
published page actually embeds.

## Marketing hand-off

The marketing site **www.solidping.io** lives in a separate repo,
`../solidping-website`, and is expected to consume the **same** assets rather
than growing its own hand-captured copies. Nothing in that repo is wired up
yet — this note is the hand-off, and this repo does not touch it.

Committed asset paths in this repo (`solidping`):

- `web/docs/static/showcase/setup-to-first-result.mp4` — AV1 recording of the
  whole flow, `docker run` to the first results
- `web/docs/static/showcase/setup-to-first-result.h264.mp4` — the H.264 twin of
  the same cut
- `web/docs/static/showcase/01-checks-list.png`
- `web/docs/static/showcase/02-check-form-filled.png`
- `web/docs/static/showcase/03-check-detail.png`

Served (from the embedded docs build) at `/docs/showcase/<file>`, e.g.
<https://solidping.io/docs/showcase/setup-to-first-result.mp4>.

**To do in `../solidping-website` when it picks these up:**

1. Copy the refreshed files into `static/img/showcase/` (or point at the served
   URLs above — the source of truth is this repo either way).
2. Give the homepage `<video>` two `<source>` children, AV1 first, exactly as
   `web/docs/docs/tour.mdx` now does — otherwise Safari without an AV1 hardware
   decoder shows the fallback text instead of the demo.
3. Stop hard-coding *"A 18-second setup"* in the caption
   (`src/pages/index.tsx`). The duration changes with every re-cut — this one is
   37.9 s — so the caption must not name one.

All three are still owed as of spec 2026-09-16-05: that repo ships a single AV1
`src=` (`src/pages/index.tsx`) and still names a duration. This repo does not
touch it.

Regenerate with `make showcase` from the `solidping` repo root. See also
[`wiki/features/showcase-media.md`](../../../wiki/features/showcase-media.md).
