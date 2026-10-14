---
model: sonnet
effort: high
---

# The published showcase is a blurry 47 s setup walkthrough: make `make showcase` produce the crisp 34 s product tour ("version A") instead

## Problem

The README and the docs Tour page play `web/docs/static/showcase/setup-to-first-result.mp4`
(46.7 s, 1280×800). Compared with "version A", the product-tour prototype the maintainer
picked on 2026-10-15, it has these problems:

1. **Blurry.** Playwright records Chromium at 1× whatever `deviceScaleFactor` says
   (`web/dash0/showcase/playwright.config.ts:79-91`), so every post-production zoom in
   `postprocess.ts` enlarges 1280×800 pixels.
2. **Thin story.** It films an empty install: a log wall (including `redis: connection
   refused` lines), ~8 s of forced password change, and a chart with four points.
3. **Small text.** Captions are small pills that cannot be read at README size.

The prototype that fixes all three is in `web/dash0/showcase/output/proposals/studio-src/`
(git-ignored, so **copy it into tracked files before anything else**):

- `rec-lib.mjs`: a recorder.
  - Stretches every beat by K (=2) and slows CSS/Web animations with CDP
    `Animation.setPlaybackRate(1/K)`.
  - Captures `Page.captureScreenshot` with `clip.scale: 2` in a loop (~40 effective fps of
    2560×1600 JPEG).
  - Writes `take.json` with frame timestamps (story ms = real ms / K), cursor path, clicks
    and focus cues. No cursor is painted into the page.
- `stage.html` + `render.mjs`: an HTML compositor rendered frame by frame through Playwright
  and piped to ffmpeg (libx264, 1920×1080, 60 fps). It draws:
  - the brand background and a browser-window frame with a fake URL bar;
  - an in-window camera with eased zooms that never crop the focus rect;
  - the painted cursor, with click ripples;
  - a caption track, a time-lapse tag, and intro/outro cards with an animated ping line.
- `take-tour.mjs` + `take-status.mjs` + `tl-tour.mjs`: version A itself.
  - Beats: dashboard (KPIs, active incident) → checks (open two groups) → check detail
    (Hour range, hover, region chips) → incidents → public status page.
  - Five captions, intro, outro with the `docker run` line.
- `tl-lib.mjs`: shared layouts, `ansiToHtml`, intro/outro cards.

The populated workspace it filmed was **hand-edited in SQL** on top of the demo seed, so it
cannot be regenerated today. The edits:
- org "Production" (slug `acme`), user "Alex Rivera";
- check names and slugs, with targets on real `https://solidping.io/...` endpoints;
- groups API / Website / Infrastructure;
- status page sections;
- backfill moved from region `default` into `eu-west` + `us-east` at the live latencies, on
  identical timestamps;
- maintenance window removed, `demo.enabled` removed.

That edit also exposed three problems in the demo seed itself
(`server/internal/jobs/jobtypes/job_startup_demo.go`):
- its status page has **no sections** ("No sections configured yet");
- the backfill (`:932-939`) writes the `default` region rather than the configured regions;
- the recurring "Weekly database maintenance" window (`:827-858`) turns the status page
  "Under Maintenance" for two hours a week.

## Proposal

1. **Bring the prototype into the tree.**
   - Move `studio-src/*` to `web/dash0/showcase/studio/` as TypeScript, matching the
     neighbours (`fixtures.ts`, `labels.ts`).
   - Resolve Playwright from the package rather than by absolute path.
   - Keep `stage.html` loading fonts from `node_modules/@fontsource-variable/*`.
2. **Recorder.**
   - Port `rec-lib.mjs` (`Recorder.launch/start/stop/cue/focus/union/moveTo/click/type/scroll/hold`).
   - K and the JPEG quality are knobs (`SHOWCASE_DILATION`, default 2).
   - Drop the `video:` block from `playwright.config.ts:91`, or keep the Playwright project
     only for the SMS still capture (`specs/sms-opt-in-consent.showcase.ts`), which needs no video.
3. **Showcase workspace seed.** Make the filmed workspace reproducible, either as a
   `showcase` profile of the demo seed or as an API-driven `fixtures.ts` step. Recommended:
   API-driven, plus one small server-side backfill hook, because the history cannot be
   written through the public API. It must produce:
   - the 10 checks and 3 groups listed above;
   - a status page with sections Website / API / Infrastructure;
   - 30 days of hourly history in **each configured region**, on shared timestamps;
   - one check flapping on `https://solidping.io/api/v1/fake?period=600`;
   - no maintenance window.

   The take waits (max 10 min) for that check to have an active incident, as
   `take-tour.mjs` does.
4. **Demo seed fixes** in `job_startup_demo.go`, independent of the video:
   - give the demo status page sections;
   - backfill every configured region (fall back to `default` only on a single-region node);
   - keep the maintenance window but not on a check the status page shows, or make it
     upcoming rather than active.
5. **Compositor.** Port `stage.html` / `render.mjs` / `tl-lib.mjs` / `tl-tour.mjs`.
   - Write the H.264 master, then the AV1 + H.264 pair the docs already serve, plus the
     three stills from named timeline points.
   - Remove the zoom/crop/label code from `postprocess.ts` that this replaces
     (`crop-window.ts`, `segment-plan.ts`, `labels.ts` and their tests), but only once nothing
     else imports them.
6. **Camera policy: far less zooming.** Maintainer feedback on the prototype: it zooms in
   and out too often.
   - The default shot is the standard full-window view, and it stays there for navigation,
     lists, charts and the status page.
   - Zoom in only while a form field is being filled (typing into an input), then return to
     the wide view.
   - Take scripts stop emitting `focus` cues for anything else. `take-tour.mjs` currently
     zooms on the KPI row, the active incidents card, the checks table, the chart and the
     incidents table: remove those cues.
   - Enforce it in the compositor: a `focus` cue is honoured only if it falls inside a typing
     span (the recorder already logs `keys`), so stray cues cannot reintroduce the old style.
7. **Make targets.** `make showcase` = seed → takes (`tour`, `status`) → render. Keep
   `showcase-cut` meaning "re-render from existing takes". `showcase-terminal` and
   `tapes/docker-run.tape` go away unless version B is kept (see Open questions).
8. **Publish A.**
   - Rename the asset to `product-tour.mp4` / `.h264.mp4` and embed it in
     `web/docs/docs/tour.mdx:51-55`.
   - In `README.md:28-33`, replace the heading with "A quick tour" and the caption with one
     line saying it is the real dashboard filmed by `make showcase`. List what is staged:
     a seeded workspace, and the `monitor.acme.com` / `status.acme.com` labels in the URL bar.
   - The README video URL is a GitHub user-attachment that only the maintainer can upload.
     Leave a `TODO(maintainer)` comment beside it.
   - Delete `setup-to-first-result.*`.
9. **Docs.** Rewrite `web/dash0/showcase/README.md` and the "Last regenerated" block of
   `wiki/features/showcase-media.md`. Cover:
   - why time dilation;
   - the two-node side-car recipe (still needed for two regions);
   - the seed;
   - measured length and sizes.

## Tests

- `web/dash0/showcase/studio/*.test.ts` (picked up by `bun run test:unit`):
  - `camState`: zoom is capped at the fit of the focus rect (no crop) and clamped to ≥ 1.
  - The camera interpolates in log-scale.
  - `localAt` / `takeTime` round-trip through multi-segment speed maps, including a cut.
  - `frameAt` never returns a frame later than the requested time.
  - `ansiToHtml` handles `2`, `22`, `0`, colour codes and escapes `<`.
- `camState` / cue filter: a `focus` cue outside a typing span is ignored (the camera stays
  wide), and one inside a typing span zooms, then the camera returns to wide after the span.
- Seed test (Go, `job_startup_demo` tests):
  - backfill rows exist for every configured region, with identical `period_start` values;
  - a single-region node still writes `default`;
  - the demo status page has ≥ 1 section;
  - no maintenance window is active at seed time on a status-page check.
- Manual:
  - `make showcase` on the two-node side-car from a blank database produces a 30–38 s cut;
  - a full-resolution frame of the chart beat is sharp at its zoom;
  - the H.264 file plays in Safari;
  - `make lint`, `bun run test:unit`, `make build-docs` all pass.

## To verify

- Whether `Animation.setPlaybackRate` survives client-side route changes in dash0 (the
  prototype reapplies it after full navigations only).
- That the chart's Day range really only draws the most recent raw points (it showed an
  empty chart for the backfilled day in the prototype). That is why the take clicks
  **Hour**. If it is a dash0 bug, file it separately.
- That nothing outside `postprocess.ts` imports `crop-window.ts` / `segment-plan.ts` / `labels.ts`.

## Open questions

- **Zoom style: wide by default with zooms only while typing (as specified), or a constant
  zoom that follows the cursor?** Recommended: wide by default, zoom only while typing. A
  follow-the-cursor camera means permanent motion and tends to crop context in a tour that
  is mostly about whole pages.

- **Keep version B (setup story) as a second, docs-only cut?** Recommended: no, ship A only.
  Its terminal scene and the `vhs` dependency can be deleted. B stays in
  `output/proposals/` for reference.
- **Show the fake `monitor.acme.com` / `status.acme.com` hosts in the URL bar, or the real
  `localhost:4000`?** Recommended: the acme hosts, disclosed in the README caption as above.
