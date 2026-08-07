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

## Running it

Needs a running SolidPing server and `ffmpeg` (with the `libsvtav1` AV1
encoder) on `PATH`:

```bash
brew install ffmpeg                  # macOS
sudo apt-get install -y ffmpeg       # Debian/Ubuntu
```

Against a side-car test server (recommended — leaves a `make dev` loop on :4000
alone):

```bash
PORT=4321 SP_RUNMODE=test SP_DB_RESET=true ./solidping serve &
E2E_BASE_URL=http://localhost:4321/dash0/ make showcase
```

Against whatever is on :4000:

```bash
make showcase
```

Useful knobs:

| Env var | Default | Meaning |
|---|---|---|
| `E2E_BASE_URL` | `http://localhost:4000/dash0/` | Server to record against (same convention as the e2e suite) |
| `SHOWCASE_SLOW_MO` | `250` | Playwright `slowMo` in ms — the "human pacing" dial |
| `SHOWCASE_ORG` / `SHOWCASE_EMAIL` / `SHOWCASE_PASSWORD` | `test` / `test@test.com` / `test` | Credentials to record with |

## What it does

1. `specs/create-http-check.showcase.ts` logs in, seeds realistic demo checks
   ("Marketing site", "Docs site", "Checkout API") through the REST API, then
   drives the create-check flow on camera: checks list → **New check** → HTTP
   type → name + target URL → interval → regions → save → check detail page.
   Named still frames are written as it goes. Seeded data is deleted afterwards.
2. `postprocess.ts` finds the recorded `.webm`, trims the frozen frames at its
   head and tail (ffmpeg `freezedetect`), re-encodes to **AV1** with
   `libsvtav1`, and copies the finished video plus the stills into
   `web/docs/static/showcase/`.

## Files

| Path | Committed? | What |
|---|---|---|
| `showcase/output/` | **no** (git-ignored) | Raw `.webm`, traces, all pipeline scratch |
| `web/docs/static/showcase/create-http-check.mp4` | yes | Trimmed AV1 recording |
| `web/docs/static/showcase/0*.png` | yes | Stills embedded in the Tour page |

Keep the committed catalog small — only assets the Tour page actually embeds.

## Marketing hand-off

The marketing site **www.solidping.io** lives in a separate repo,
`../solidping-website`, and is expected to consume the **same** assets rather
than growing its own hand-captured copies. Nothing in that repo is wired up
yet — this note is the hand-off.

Committed asset paths in this repo (`solidping`):

- `web/docs/static/showcase/create-http-check.mp4` — AV1 screen recording of
  the HTTP-check creation flow
- `web/docs/static/showcase/01-checks-list.png`
- `web/docs/static/showcase/02-check-form-filled.png`
- `web/docs/static/showcase/03-check-detail.png`

Served (from the embedded docs build) at `/docs/showcase/<file>`, e.g.
<https://solidping.io/docs/showcase/create-http-check.mp4>.

Regenerate with `make showcase` from the `solidping` repo root. See also
[`wiki/features/showcase-media.md`](../../../wiki/features/showcase-media.md).
