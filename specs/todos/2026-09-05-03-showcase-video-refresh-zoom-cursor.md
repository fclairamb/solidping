---
model: opus
effort: high
---

# The showcase video is a month stale, has no zoom, no cursor and robotic typing — it does not look slick

## Problem

`web/docs/static/showcase/create-http-check.mp4` (and the three stills next to
it) were generated once, on 2026-08-07 (`ec971d676`), and never regenerated.
They are embedded on the docs [Tour page](../../web/docs/docs/tour.mdx) and,
via a hand-copied duplicate under `static/img/showcase/`, on the marketing
homepage (`../solidping-website/src/pages/index.tsx:108-118`, captioned
*"A 18-second setup"*). The pipeline in `web/dash0/showcase/` exists precisely
so this media can be re-cut instead of rotting, but it has not been run since,
and the UI has moved a lot in the meantime (#246 alone shipped status-page
branding, audit log, SLOs, path tracing…).

Two things are wrong, and the overall objective is one: **demo videos that are
as slick, smooth and shiny as we can make them.**

### 1. It shows an old version of the app, and the documented recipe no longer works

- The recording is of the early-August dash0. Every selector the spec relies
  on still exists (`new-check-button`, `check-type-select`, `check-url-input`,
  `check-period-select`, `region-option-*`, `check-submit-button`), so the
  script most likely still drives the UI — it just needs to be run again.
- The README's recommended recipe — a disposable side-car server on a fresh
  SQLite directory in **default** run mode — is broken since 2026-08-25:
  the seeded `admin@solidping.io` now boots with `MustChangePassword = true`
  (`server/internal/jobs/jobtypes/job_startup.go:253`, spec 2026-08-23-04).
  `apiLogin()` (`web/dash0/showcase/fixtures.ts:95`) gets a token that can only
  reach `/auth/change-password`, `/auth/me` and `/auth/logout`, so the very
  next call — `POST /api/v1/orgs` in `ensureCleanShowcaseOrg()` — answers
  `403 PASSWORD_CHANGE_REQUIRED`, and `uiLogin()` (`fixtures.ts:293`) would
  land on `/dash0/change-password` and time out on `waitForURL`. Nothing in
  the pipeline handles this state; the README and
  `wiki/features/showcase-media.md` still describe the pre-rotation flow.

### 2. Nothing in the recording draws the eye — it reads as a screen capture, not a demo

Looking at `web/dash0/showcase/playwright.config.ts` and
`specs/create-http-check.showcase.ts`:

- **No zoom.** The whole flow is one static 1280×800 frame. When the script
  types a URL into a field that is 300 px wide, the viewer sees a full
  dashboard with a tiny change in one corner. The user's request is explicit:
  *make it zoom sometimes*.
- **No cursor.** Headless Chromium does not paint the pointer into the
  screencast, so buttons "click themselves". Without a visible pointer a zoom
  has nothing to follow.
- **Robotic input.** Text is injected with `fill()`
  (`create-http-check.showcase.ts:63-65`) — the URL and the name appear in a
  single frame. Options are clicked with no travel.
- **Blurry if we zoom naïvely.** `deviceScaleFactor: 1` and a 1280×800 video
  mean any post-production zoom is an upscale of 1× pixels.
- **Pacing is a blunt global `slowMo: 250`** applied to every Playwright
  action, rather than deliberate beats around the moments that matter.
- **Playback reach.** The only encode is AV1 (`libsvtav1`). Safari decodes AV1
  only on Apple-silicon machines with the hardware decoder, so a chunk of
  homepage visitors see the `<video>` element's fallback text. A demo that
  does not play is the opposite of shiny.

## Proposal

Keep the pipeline exactly where and what it is — a manual `make showcase`,
outside the e2e suite, provisioning its own `northwind` org — and make it
produce a video worth watching. The recording is **regenerated as part of this
change** and the new assets committed.

### A. Make the pipeline run again on the current app

1. **Handle the forced password rotation in the bootstrap.** After
   `apiLogin()`, probe `GET /api/v1/auth/me`; when the account is flagged
   `mustChangePassword`, call `POST /api/v1/auth/change-password` and carry the
   rotated password through both the API bootstrap and `uiLogin()`.
   `web/dash0/e2e/forced-password-change.spec.ts` shows the state machine. Use
   a deterministic pipeline-chosen value (`SHOWCASE_ROTATED_PASSWORD`, default
   documented in the README); check whether the endpoint refuses re-using the
   current password before defaulting to `solidpass` again. Log clearly that
   the rotation happened and that the side-car account now has that password.
2. **Re-verify the flow against today's UI** and fix whatever moved (option
   labels, the type-picker placeholder `Search check types...`, the detail
   page's settle time). Refresh the `alt` texts in `tour.mdx` to describe what
   is actually in the new stills.
3. Update `web/dash0/showcase/README.md` and `wiki/features/showcase-media.md`
   for the rotation step and the new knobs below.

### B. Zoom — recorded at 2×, choreographed in post from cue points

Zooming is done **in post-production, not in the browser**, so the UI is never
touched and the zoom is smooth regardless of Playwright's action cadence:

1. **Record at 2× so zooms stay crisp.** In `playwright.config.ts` set
   `deviceScaleFactor: 2` and `video.size` to `2560×1600` while keeping the
   viewport at `1280×800` CSS px. Output stays 1280×800, so a zoom of up to 2×
   is pixel-exact. Stills keep being published at 1280×800 (downscale in
   `postprocess.ts`) — the committed catalog must stay small.
2. **A `focus()` helper writes cue points.** Add to `fixtures.ts` something
   like `focus(page, locator | null, { zoom?, ease?, hold? })` that records
   `{ t, x, y, w, h, zoom }` (target rectangle in CSS px from
   `boundingBox()`, time relative to a recording anchor) into
   `output/cues/<recording>.json`. `focus(page, null)` returns to the full
   frame. The spec calls it at the moments listed below; the recording itself
   is unchanged by it.
3. **Timeline anchor.** Playwright does not expose when the video's first frame
   was captured, but the recording opens on a blank frame that
   `postprocess.ts` already locates with `freezedetect` (`trimWindow()` →
   `leading.end`). The spec takes its `t = 0` right after the first real
   navigation resolves (the login page appearing is what ends that freeze), and
   `postprocess.ts` maps cue time `t` to `leading.end + t`, then into the
   trimmed timeline (`- window.start`). Provide `SHOWCASE_CUE_OFFSET_MS` to
   nudge the alignment by hand if a run drifts.
4. **Cue points → ffmpeg.** In `postprocess.ts`, a pure function turns the cue
   list into a per-frame crop window: between cues, interpolate the centre and
   zoom with an ease-in-out curve over a fixed transition (~600 ms), hold
   otherwise, keep the 16:10 aspect, and clamp the window inside the source.
   Emit it as `crop=w=…:h=…:x=…:y=…` expressions over `t` (or a `sendcmd`
   file — implementer's call), followed by `scale=1280:800:flags=lanczos`,
   applied inside the existing trim + encode. Keep the generator a pure,
   unit-tested function: cue in, window-per-time out, with tests for clamping
   at the frame edges, aspect preservation, easing monotonicity, and "no cues"
   producing the identity crop. Those are the parts that can be tested
   without a browser.
5. **Choreography for `create-http-check`** (the implementer tunes the numbers
   by eye):
   - checks list: full frame, hold for the still;
   - **New check** button: gentle push-in (~1.4×) as the cursor travels to it,
     back to full frame as the form loads;
   - check-type picker: ~1.6× on the combobox while `http` is typed and picked;
   - URL + name: ~1.5× framing both fields while the text is typed;
   - interval + regions: medium zoom on that section;
   - **full frame before Save** — never zoom across a route change;
   - detail page: full frame for the still, then a slow Ken-Burns push-in
     (~1.15×) toward the status / response-time area, easing back out before
     the end so the loop point is seamless.
   Rules of thumb: ≤ 1.8× (2× is the crisp ceiling), transitions 500–700 ms,
   ≥ 1.5 s hold between transitions, zoom follows the cursor and never leads it.

### C. Cursor and human input — the cheap wins that make the zoom read

1. **Synthetic cursor overlay.** Headless Chromium paints no pointer, so inject
   one via `context.addInitScript`: a fixed-position, `pointer-events: none`
   arrow (SVG, ~24 px at 1× so 48 px in the 2× recording) that follows
   `mousemove`, with a short click ripple on `mousedown`. Gate it on an env
   flag (`SHOWCASE_CURSOR=1`, on by default for the showcase project) so the
   SMS opt-in captures — which are evidence for a carrier reviewer and must not
   be dressed up (`specs/sms-opt-in-consent.showcase.ts:32-35`) — keep
   recording exactly the shipped UI with the overlay off.
2. **Move, then click.** Add a `moveTo(page, locator)` helper that
   `page.mouse.move()`s to the element centre in eased steps (~350–500 ms
   travel) before every click; use it throughout `create-http-check`.
3. **Type like a person.** Replace `fill()` with `pressSequentially()` at
   ~40–70 ms per character (URL, name, `http` in the type search).
4. **Drop the global `slowMo` in favour of explicit beats.** `slowMo` stacks
   on every input step and would turn eased cursor travel into a crawl; keep
   `SHOWCASE_SLOW_MO` as an escape hatch defaulting to `0`.

### D. Encode so it plays everywhere, and hand it off

1. `postprocess.ts` emits the AV1 file as today **plus** an H.264 fallback
   (`libx264`, `yuv420p`, `+faststart`, tuned for a tiny screen-capture file).
   `tour.mdx` switches the `<video>` to two `<source>` children, AV1 first.
   Note the same change for the website's `<video>` in the hand-off.
2. Regenerate: `make showcase` against a fresh side-car (the README recipe,
   now working), commit the new `web/docs/static/showcase/*`, and, because
   this is a visual deliverable CI cannot judge, attach to the PR a contact
   sheet of frames (`ffmpeg -vf "select=…,tile=…"`) proving the zooms frame
   what they should, plus the new duration and file sizes.
3. Hand-off list in the README for `../solidping-website` (separate repo,
   not touched here): copy the new assets to `static/img/showcase/`, add the
   H.264 `<source>`, and stop hard-coding *"18-second"* in the caption.

### Out of scope

- CI or scheduled regeneration (still deliberately manual).
- New showcase flows beyond the existing create-HTTP-check one; the zoom /
  cursor helpers are built so the next flow can use them.
- Motion-interpolating Playwright's 25 fps screencast to 60 fps
  (`minterpolate`) — worth a five-minute experiment on the way, keep it only
  if it is artifact-free, otherwise leave it out and note the result.

### Open questions

- Does `POST /auth/change-password` reject reusing the current password? That
  decides whether the side-car account ends up on `solidpass` or on the
  rotated value; either way it is documented, not guessed.
- Published stills at 1280×800 (as today) vs 2560×1600 for retina — default to
  1280×800 unless the 2× PNGs stay under ~250 KB each.

## Resolved open questions

> Does `POST /auth/change-password` reject reusing the current password? That
> decides whether the side-car account ends up on `solidpass` or on the
> rotated value; either way it is documented, not guessed.

**Resolved: yes, reuse is rejected — so the side-car account ends up on the
rotated value, never back on `solidpass`.** Verified in the code, not assumed:
`server/internal/handlers/auth/change_password_handler_test.go:137-142` pins the
case `{"currentPassword":"testpass1234","newPassword":"testpass1234"}` to
`400 VALIDATION_ERROR` ("unchanged password maps to VALIDATION_ERROR"), and the
check runs in the handler before the service is reached.

Directive for the implementer: do not attempt to rotate `solidpass` back to
itself — it will 400. `SHOWCASE_ROTATED_PASSWORD` must default to a value
*different* from the seeded `solidpass`, and both the bootstrap (`apiLogin()`)
and `uiLogin()` must carry that rotated value for the rest of the run. Document
the default in `web/dash0/showcase/README.md` and
`wiki/features/showcase-media.md`, along with the fact that the side-car account
is left on it.

> Published stills at 1280×800 (as today) vs 2560×1600 for retina — default to
> 1280×800 unless the 2× PNGs stay under ~250 KB each.

**Resolved: publish at 1280×800.** Measure the 2× PNGs during the run; only
switch to 2560×1600 if every one of them lands under ~250 KB. Report the
measured sizes either way — the committed catalog staying small is the
constraint that decides it, so state the numbers rather than the conclusion
alone.
