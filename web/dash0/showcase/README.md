# Showcase media pipeline

Drives the **real dash0 UI** with Playwright and produces the screenshots and
screen recording embedded in the docs [Tour page](../../docs/docs/tour.mdx).

The point is that the media is **regenerable**: when the UI changes, re-run the
pipeline instead of hand-recapturing, so the published assets can never quietly
rot.

## This is not a test suite

The showcase project lives outside `web/dash0/e2e/` on purpose:

- the e2e config (`web/dash0/playwright.config.ts`) pins `testDir: "./e2e"`, so
  `bunx playwright test` — and therefore CI — never sees these files;
- the recordings are named `*.showcase.ts`, which does not match Playwright's
  default `testMatch` either.

There is **no CI job and no scheduled regeneration**. It is a manual
`make showcase` only.

The one part of it that *is* covered by CI is `crop-window.ts`, the pure
cue-list → crop-window generator: `bun run test:unit` picks up
`showcase/**/*.test.ts`. Everything else needs a browser and ffmpeg, so it is
checked by looking at the output.

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
2026-08-25 and this change that silently broke the side-car recipe below: the
run died on its very next call, `POST /api/v1/orgs`.

`apiLogin()` now handles it:

- it detects the flag (on the login response, double-checked against
  `GET /auth/me`) and rotates the password to `SHOWCASE_ROTATED_PASSWORD`;
- **the account stays on that password.** It cannot be rotated back:
  `POST /auth/change-password` refuses a new password equal to the current one
  with `400 VALIDATION_ERROR` ("new password must be different from the current
  one", pinned by `server/internal/handlers/auth/change_password_handler_test.go`).
  That is why the default is `showcase-rotated-pass` and not `solidpass`;
- a rerun against a database this pipeline already rotated works: the seeded
  password is tried first, and the rotated one on a `401`.

Both facts are logged on every run, so nobody has to read this file to find out
why their side-car's admin password changed. If you point the pipeline at a
server you care about, that password change is permanent — one more reason to
use the disposable side-car.

## Running it

Needs a running SolidPing server and `ffmpeg` on `PATH`, built with **both**
`libsvtav1` (AV1) and `libx264` (the H.264 fallback):

```bash
brew install ffmpeg                  # macOS
sudo apt-get install -y ffmpeg       # Debian/Ubuntu
```

Against a **disposable side-car server** — the recommended way, for data-safety
reasons as much as to leave a `make dev` loop on :4000 alone (see the warning
below). Note: **default** run mode, no `SP_RUNMODE=test`:

```bash
mkdir -p /tmp/showcase-db
PORT=4321 SP_DB_TYPE=sqlite SP_DB_DIR=/tmp/showcase-db ./solidping serve &
E2E_BASE_URL=http://localhost:4321/dash0/ make showcase
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
| `E2E_BASE_URL` | `http://localhost:4000/dash0/` | Server to record against (same convention as the e2e suite) |
| `SHOWCASE_BOOTSTRAP_ORG` / `SHOWCASE_EMAIL` / `SHOWCASE_PASSWORD` | `default` / `admin@solidping.io` / `solidpass` | Account used to bootstrap; also the identity that appears on camera |
| `SHOWCASE_ROTATED_PASSWORD` | `showcase-rotated-pass` | Password the account is rotated onto when the server forces a rotation, and **stays on**. Must differ from `SHOWCASE_PASSWORD` and be ≥ 8 characters |
| `SHOWCASE_ORG` / `SHOWCASE_ORG_NAME` | `northwind` / `Northwind Systems` | The org that gets provisioned and filmed |
| `SHOWCASE_USER_NAME` | `Alex Rivera` | Display name shown in the sidebar footer |
| `SHOWCASE_CURSOR` | on | Set to `0` to record without the synthetic pointer |
| `SHOWCASE_TRAVEL_MS` | `420` | How long the cursor takes to travel to a control before clicking it |
| `SHOWCASE_SLOW_MO` | `0` | Playwright `slowMo`. Escape hatch only — it delays *every* input step, including each step of the eased cursor travel |
| `SHOWCASE_CLAPPER_MS` | `320` | How long the black sync clapper covers the frame |
| `SHOWCASE_CUE_OFFSET_MS` | `0` | Nudge the whole zoom timeline earlier/later if a run drifted |

## What it does

1. `specs/create-http-check.showcase.ts` bootstraps and cleans the showcase org
   (above), seeds realistic demo checks ("Marketing site", "Docs site",
   "Checkout API") through the REST API, logs in through the real form, then
   drives the create-check flow on camera: checks list → **New check** → name
   + target URL → interval → (regions, if offered) → save → check detail page.
   Named still frames are written as it goes. The org is emptied afterwards.

   > **There is deliberately no check-type step.** The form defaults to HTTP
   > (`initialType = initialData?.type || "http"`, `check-form.tsx`), so
   > opening the combobox to choose the value already selected filmed "HTTP" →
   > "HTTP" — dead time. The spec asserts the default instead, so a change to
   > it fails the run rather than quietly filming the wrong type.

   > **The regions beat only fires against a server that offers more than one
   > region.** The form renders the region picker only when
   > `availableRegions.length > 1` (`web/dash0/src/components/shared/check-form.tsx`),
   > and the spec gates the whole beat — cue included — on `regionCount > 0`.
   > A single-node side-car offers one region, so it silently records no
   > regions step and the cue list goes straight from `interval` to
   > `form-complete`. That is expected, not a bug: **the currently committed cut
   > has no regions beat.** If you want one on camera, film against a server
   > with at least two regions configured.
2. `postprocess.ts` finds that recording (by spec name — the SMS capture below
   records a video too), trims it, applies the camera move, and encodes it as
   **AV1** *and* **H.264** into `web/docs/static/showcase/`.

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
| `showcase/output/` | **no** (git-ignored) | Raw `.webm`, cue lists, 2× stills, all pipeline scratch |
| `web/docs/static/showcase/create-http-check.mp4` | yes | Trimmed AV1 recording |
| `web/docs/static/showcase/create-http-check.h264.mp4` | yes | Same cut in H.264, for browsers without AV1 |
| `web/docs/static/showcase/0*.png` | yes | Stills embedded in the Tour page |

Keep the committed catalog small — only assets the Tour page actually embeds.

## Marketing hand-off

The marketing site **www.solidping.io** lives in a separate repo,
`../solidping-website`, and is expected to consume the **same** assets rather
than growing its own hand-captured copies. Nothing in that repo is wired up
yet — this note is the hand-off, and this repo does not touch it.

Committed asset paths in this repo (`solidping`):

- `web/docs/static/showcase/create-http-check.mp4` — AV1 screen recording of
  the HTTP-check creation flow
- `web/docs/static/showcase/create-http-check.h264.mp4` — **new**, the H.264
  twin of the same cut
- `web/docs/static/showcase/01-checks-list.png`
- `web/docs/static/showcase/02-check-form-filled.png`
- `web/docs/static/showcase/03-check-detail.png`

Served (from the embedded docs build) at `/docs/showcase/<file>`, e.g.
<https://solidping.io/docs/showcase/create-http-check.mp4>.

**To do in `../solidping-website` when it picks these up:**

1. Copy the refreshed files into `static/img/showcase/` (or point at the served
   URLs above — the source of truth is this repo either way).
2. Give the homepage `<video>` two `<source>` children, AV1 first, exactly as
   `web/docs/docs/tour.mdx` now does — otherwise Safari without an AV1 hardware
   decoder shows the fallback text instead of the demo.
3. Stop hard-coding *"A 18-second setup"* in the caption
   (`src/pages/index.tsx`). The duration changes with every re-cut — this one is
   ~28 s — so the caption must not name one.

Regenerate with `make showcase` from the `solidping` repo root. See also
[`wiki/features/showcase-media.md`](../../../wiki/features/showcase-media.md).
