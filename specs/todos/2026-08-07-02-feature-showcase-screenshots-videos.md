---
model: opus
effort: high
---

# No visual feature showcase — the product is only described in text, never shown

## Problem

Neither the marketing site nor the docs ever *show* the product:

- The marketing website (`../solidping-website`, www.solidping.io — Docusaurus,
  pages `src/pages/index.tsx`, `compare/`, `saas/`) pitches features in text
  and static imagery only.
- The embedded docs site (`web/docs`, served at `/docs` on every host) has no
  screenshots or videos of the dashboard either.

A prospective user cannot see what creating a check, reading a check detail
page, or a status page actually looks like without signing up first.

Hand-captured media would rot: the dash0 UI changes constantly, and stale
screenshots are worse than none. Whatever we capture must be **regenerable
from the real UI on demand**.

## Proposal

Build a showcase media pipeline in this repo that drives the real dash0 UI
with Playwright and produces polished screenshots and screen-recording videos,
then surface them in a feature-showcase page.

### 1. Media-generation pipeline (this repo)

Playwright already records video — `web/dash0/playwright.config.ts:47` sets
`video: "retain-on-failure"` for e2e — so the machinery exists. Add a
dedicated **showcase** Playwright project (separate from `web/dash0/e2e/`, not
part of the test suite) with:

- **Staged demo data**: a clean org with realistic names ("Production API",
  "Marketing site"…), not the `test@test.com` fixtures — seed via the API the
  way e2e fixtures do (`web/dash0/e2e/fixtures.ts`).
- **Human pacing**: deliberate `slowMo` / scripted pauses so a viewer can
  follow the flow; fixed viewport (e.g. 1280×800); consistent theme.
- **Video always on** (`video: "on"`) and named screenshot steps, output to a
  predictable directory (e.g. `web/dash0/showcase/output/`).
- **Post-processing**: Playwright emits `.webm` (VP8); re-encode to **AV1**
  (ffmpeg with `libsvtav1`) for the published assets — best compression at
  screen-capture quality — and trim dead frames at start/end. Screen recordings
  of a UI compress extremely well under AV1, keeping committed assets tiny.

### 2. First video: creating an HTTP check

Script the canonical flow through
[`web/dash0/src/routes/orgs/$org/checks.new.tsx`](../../web/dash0/src/routes/orgs/$org/checks.new.tsx)
(existing specs like `web/dash0/e2e/check-http-expected-status-codes.spec.ts`
already exercise the selectors):

1. Checks list → "New check"
2. Fill name + target URL, pick HTTP type
3. Choose interval / regions
4. Save → land on the check detail page with first results coming in

Also capture still screenshots of the key frames (form filled, detail page)
as a byproduct of the same run.

### 3. Showcase surface

Generate the assets here; embed them where visitors look. Start with a
features/showcase section — candidate homes:

- `../solidping-website` (marketing — likeliest final home), or
- `web/docs` (a "Tour" / getting-started page with the video inline).

## Open questions

- **Where does the showcase page live** — marketing website, docs site, or
  both (same assets)? The spec's deliverable can stop at "assets generated +
  embedded in `web/docs`" with the website embedding done in that repo later.
- **Asset hosting**: commit the AV1 videos + pngs (small, few MB) or push to
  a CDN? Committed-to-website is simplest; watch repo bloat if the catalog
  grows.
- **Regeneration cadence**: manual `make showcase` target first; CI
  regeneration can come later once the flows are stable.

## Resolved open questions

> **Where does the showcase page live** — marketing website, docs site, or
> both (same assets)?

**Decision: `web/docs` only — plus a hand-off note for the marketing repo.**
Build the showcase page in `web/docs` (a "Tour" / getting-started page with the
video and stills inline). Do **not** edit `../solidping-website` in this spec —
it is a separate repo and out of scope for this change.

Instead, record in this repo that the marketing site should pull the same
assets: add a short section to the engineering notes under `wiki/` (and a brief
note in the showcase pipeline's own README, wherever the pipeline lives) stating
that `www.solidping.io` is expected to consume the assets generated here, giving
their committed paths and how to regenerate them. That note is the deliverable
for the marketing side — the actual embedding happens later in the
`solidping-website` repo.

> **Asset hosting**: commit the AV1 videos + pngs (small, few MB) or push to a
> CDN?

**Decision: commit the generated AV1 videos and PNGs to this repo.** No CDN
plumbing. Commit them under the docs site so they ship with the embedded
`web/docs` build (no extra infra). Keep the committed catalog deliberately small
— only the assets actually embedded in the showcase page. Do not commit raw
Playwright `.webm` intermediates or other pipeline scratch output; the pipeline's
working/output directory must be git-ignored, and only the post-processed,
published assets get committed. If the catalog later grows large enough to bloat
the repo, that is a follow-up spec, not this one.

> **Regeneration cadence**: manual `make showcase` target first; CI regeneration
> can come later once the flows are stable.

**Decision: manual only.** Ship a `make showcase` target that runs the pipeline
end to end (seed demo data → drive the UI → record → re-encode to AV1 → write
the published assets). Do **not** add any CI job, scheduled workflow, or
regeneration check in this spec — CI regeneration is explicitly out of scope
until the flows have proven stable.

## Implementation Plan

1. **`web/dash0/showcase/` — a standalone Playwright project**, deliberately
   outside `web/dash0/e2e/` so `bunx playwright test` (whose `testDir` is
   `./e2e`) and therefore CI never see it.
   - `playwright.config.ts`: `testDir: "./specs"`, `testMatch: "**/*.showcase.ts"`,
     `video: "on"`, fixed `viewport: 1280x800`, `launchOptions.slowMo` for human
     pacing, `colorScheme: "light"` for a consistent theme, single worker, no
     retries, `outputDir: "./output/run"`.
   - `fixtures.ts`: `API_BASE` derived from `E2E_BASE_URL` exactly like
     `web/dash0/e2e/fixtures.ts`; API login + demo-data seeding/cleanup helpers.
2. **`specs/create-http-check.showcase.ts`** — seeds realistic demo checks
   ("Production API", "Marketing site", "Checkout API") through the REST API,
   then drives the real UI: checks list → "New check" → name + URL + HTTP type →
   interval + regions → save → check detail page. Named screenshot steps write
   stills (`01-checks-list`, `02-form-filled`, `03-check-detail`) into
   `output/stills/`. Seeded demo data is deleted afterwards.
3. **`postprocess.ts`** — bun script: locates the recorded `.webm`, uses
   `ffmpeg freezedetect` to trim dead frames at head/tail, re-encodes to **AV1**
   via `libsvtav1` into an `.mp4`, and copies the trimmed video + the chosen
   stills into `web/docs/static/showcase/`. If `ffmpeg` is missing or has no
   `libsvtav1` encoder it exits with an explicit, actionable message
   (`brew install ffmpeg` / `apt install ffmpeg`), never a raw ENOENT.
4. **`make showcase`** — one target running the whole pipeline (seed → drive →
   record → re-encode → publish assets). No CI wiring at all.
5. **`web/docs/docs/tour.mdx`** — a "Tour" page (sidebar position right after
   the intro) embedding the AV1 video with `useBaseUrl` plus the stills. Builds
   under `make build-docs` and appears in the autogenerated sidebar.
6. **Committed assets**: only `web/docs/static/showcase/*.mp4` + `*.png` that
   the Tour page embeds. `web/dash0/showcase/output/` (raw `.webm`, traces,
   scratch) is git-ignored.
7. **Marketing hand-off note**: a section in `wiki/features/showcase-media.md`
   and a matching note in `web/dash0/showcase/README.md`, both stating that
   `www.solidping.io` (`../solidping-website`, separate repo) is expected to
   consume these assets, listing their committed paths and the `make showcase`
   regeneration command. `../solidping-website` itself is not touched.

QA: `make build-dash0` + `cd web/dash0 && bun run lint` (no NEW errors),
`make build-docs`, and one real end-to-end run of `make showcase` against a
side-car `SP_RUNMODE=test` server on a non-default port.
