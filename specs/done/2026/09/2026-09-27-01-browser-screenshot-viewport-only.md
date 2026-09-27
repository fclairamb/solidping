---
effort: medium
---

# Full-page browser screenshots stall and fail silently: capture the viewport only

## Problem

Every browser screenshot is a full-page capture. `Session.Screenshot` asks CDP for
`page.CaptureScreenshot().WithCaptureBeyondViewport(true).WithFromSurface(true)`, WebP q85
(`server/internal/checkers/checkbrowser/session.go:682-709`). On a long page the renderer
has to rasterize the whole document height, and that sometimes never finishes inside the
5s `ScreenshotTimeout` (`server/internal/checkers/checkbrowser/config/config.go:35`).

Observed on solidping.k8xp.com on 2026-09-26, a browser check on `https://www.acme.com`
(`screenshot: true`, `timeout: 25s`, region paris):

- The first "Capture now" (22:50:31Z) was claimed 70ms later. The run was `up`, then the
  capture failed with its full budget still available at the start:
  ```
  level=WARN msg="browser check: screenshot capture failed" url=https://www.acme.com error="context deadline exceeded" budget_remaining_ms=4999
  ```
  This is a stall inside the capture, not the starvation fixed by spec 2026-09-25-35.
- The second click (22:52:08Z) succeeded, the capture taking about 2.8s. The stored image
  is 785×9946 px, 566 KB.

Reproduced against the paris browser container over a port-forward, fresh browser context
per attempt, same CDP call as production:

| Variant | Result |
|---|---|
| Full page, WebP q85 (production) | 3 of 4 still running after 55s, 1 took 2.2s |
| Full page, PNG | 3/3 in 1.3-2s, ~930 KB |
| Viewport only, WebP q85 | 3/3 in ~1s, 43 KB |

The page draws WebGL canvases. With `--disable-gpu` Chrome renders them in software
(SwiftShader) and the browser logs `GPU stall due to ReadPixels`. That is the likely reason
a ~10k px surface stalls. It is plausible, not proven, and it does not matter for the fix:
any page can be arbitrarily tall.

Three more problems came out of the same investigation:

1. **Memory.** The hung full-page captures pushed the browser container past its 512Mi limit
   and it was OOMKilled. The check run in flight at that moment landed `down`.
2. **No viewport is set.** Nothing in `checkbrowser` or `checkjs` calls
   `emulation.SetDeviceMetricsOverride` (or equivalent), so Chrome's headless default of
   800×600 applies. That is where the 785 px width comes from (800 minus the scrollbar).
   Pages lay out at a width some sites treat as tablet or mobile.
3. **A failed on-demand capture is silent.** The run is stored without an image and the
   only trace is the WARN line. `CheckScreenshotsCard`
   (`web/dash0/src/components/checks/check-screenshots-card.tsx:29`, `:96`) keeps polling for
   3 minutes (`PENDING_TIMEOUT_MS`) and then toasts "still waiting". The operator never
   learns that the capture ran and failed.

## Proposal

### 1. Viewport-only captures

`Session.Screenshot` drops `WithCaptureBeyondViewport(true)` and captures the current
viewport. Keep WebP q85 and the `MaxScreenshotBytes` cap.

This one method is shared by the browser checker's failure/on-demand capture
(`server/internal/checkers/checkbrowser/checker.go:271-340`) and the JS runtime's
`page.screenshot()` (`server/internal/checkers/checkjs/browser.go:270`), so both switch.
Viewport-only is also the Playwright and Puppeteer default for `page.screenshot()`.

What is lost: a failure below the first screen (a missing keyword in the footer, a
`waitSelector` target further down) is not visible in the image. The check's error output
already names what failed, so this is accepted.

### 2. Explicit desktop viewport: 1280×800

Every browser session gets a 1280×800 viewport (device scale factor 1, not mobile) before
its first navigation. The natural place is session open
(`server/internal/checkers/checkbrowser/session.go:159-224`, after the eager allocate and
before the `Session` is returned), so the browser checker and JS checks both get it. Define
the size as named constants next to the other capture constants in `checkbrowser`.

The viewport must be in place before `Navigate` (`session.go:342`), so the page lays out at
1280 px from the start, not resized after load.

Check that existing keyword / `waitSelector` / JS-check tests still pass. A wider layout can
change which elements are visible. If a test depended on the 800 px layout, fix the test,
and state it in the PR.

### 3. Surface a failed on-demand capture

When a run carries an on-demand request (the claim set `capture_claimed_at`, see
`server/internal/checkworker/checkjobsvc/service.go:990-1040`) and no screenshot comes
back, the operator must be told instead of waiting 3 minutes.

- **Checker/worker:** record the capture failure on the result. Add a serialized field to
  `checkerdef.Diagnostics` (`server/internal/checkers/checkerdef/diagnostics.go:20`), e.g.
  `ScreenshotError string`, set where `captureScreenshot` logs the WARN today
  (`checker.go:316-321`), and when a capture is dropped over cap. It must be serialized so a
  deported agent's result frame carries it. Only set it when a capture was actually
  attempted.
- **Server:** in both result-submission paths (the ones calling `CheckJob.HonorOnDemand`,
  `server/internal/db/models/check_job.go:159-169`), when the lease carried a capture request
  and the result has no screenshot, persist the outcome of that request: its `requestedAt`,
  `failed`, and the error message. Store it on the check job row (new columns, migrations
  for both Postgres and SQLite) or wherever fits the existing screenshot model best. Use one
  place, not both.
- **API:** expose the outcome of the latest on-demand request so the card can match it to
  the `requestedAt` returned by `POST .../screenshots/capture`
  (`server/internal/handlers/checkscreenshots/service.go:124`, `:179`). Either add it to the
  `GET .../screenshots` list response (next to `data`, not inside it), or add a small
  `GET .../screenshots/capture` status endpoint. Document it in
  `server/internal/app/openapi/openapi.yaml` and `wiki/api-specification/`.
- **dash0:** while waiting, `CheckScreenshotsCard` also reads that outcome. When the pending
  request is reported failed, stop polling and show an error toast ("The capture failed:
  <reason>"), with a new locale key in every locale. Keep the 3-minute timeout for the case
  where the run never happens.

The failure-screenshot path (a check opted into `screenshot: true` that fails) gets the same
`ScreenshotError` on its result for free. Showing it on the incident page is out of scope.

### 4. Fix the timing comment

`ScreenshotTimeout`'s comment (`config.go:24-35`) says a real capture is "measured around
0.34s for a typical page". Update it with the viewport-only numbers above and the reason
full-page was dropped. Keep 5s.

### 5. Docs and changelog

- `web/docs/docs/features/javascript-checks.md` (`page.screenshot()`, around line 350) and
  the browser check page in `web/docs/docs/features/check-types.md`: screenshots show the
  first screen at 1280×800, not the full page.
- `CHANGELOG.md` entry per `wiki/conventions/changelog.md`: behaviour change, screenshots are
  now viewport-only at 1280×800, and a failed "Capture now" is reported.

### Out of scope

- An opt-in `page.screenshot({ fullPage: true })` for JS checks.
- Raising the browser container memory limit on k8xp (512Mi).
- Enabling GPU or `--enable-unsafe-swiftshader` on the browser sidecar.

## Tests

- `checkbrowser`: a test proving the capture request does not set `captureBeyondViewport`
  (drive it through the existing seam, or assert on the CDP params), and that the session
  sets the 1280×800 viewport before the first navigation. Include a positive control: an
  image taken on a page taller than 800 px has a height of 800 px (device pixels at scale 1).
- `checkbrowser`: a capture that errors or exceeds `MaxScreenshotBytes` sets
  `Diagnostics.ScreenshotError`. A run that never attempts a capture leaves it empty.
- Server: an on-demand run whose result has no screenshot records a failed outcome for that
  `requestedAt`. A successful capture does not. A result from a lease that carried no
  request records nothing (the same guard as `HonorOnDemand`). Run on both SQLite and
  Postgres.
- dash0: unit and/or Playwright coverage of the card switching from "capturing" to the
  failure message when the outcome says failed, and not before.
