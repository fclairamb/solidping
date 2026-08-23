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

## The recording org — nothing on camera is a test fixture

These frames get published, so they must not advertise our test rig. The
pipeline therefore **provisions its own organization** rather than filming in
an existing one:

1. it logs in with the bootstrap account and calls `POST /api/v1/orgs` to
   create the org **Northwind Systems** (slug `northwind`) — brand new, so it
   contains no data whatsoever;
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

## Running it

Needs a running SolidPing server and `ffmpeg` (with the `libsvtav1` AV1
encoder) on `PATH`:

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
> The pipeline **writes to whatever server you point it at**, and one of those
> writes is permanent:
>
> - **The `northwind` / "Northwind Systems" organization persists.** It is
>   created on the first run and never deleted — there is no delete-org
>   endpoint, and reruns deliberately reuse it. Point the pipeline at your dev
>   database and that org is in your org switcher from then on.
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
| `SHOWCASE_SLOW_MO` | `250` | Playwright `slowMo` in ms — the "human pacing" dial |
| `SHOWCASE_BOOTSTRAP_ORG` / `SHOWCASE_EMAIL` / `SHOWCASE_PASSWORD` | `default` / `admin@solidping.io` / `solidpass` | Account used to bootstrap; also the identity that appears on camera |
| `SHOWCASE_ORG` / `SHOWCASE_ORG_NAME` | `northwind` / `Northwind Systems` | The org that gets provisioned and filmed |
| `SHOWCASE_USER_NAME` | `Alex Rivera` | Display name shown in the sidebar footer |

## What it does

1. `specs/create-http-check.showcase.ts` bootstraps and cleans the showcase org
   (above), seeds realistic demo checks ("Marketing site", "Docs site",
   "Checkout API") through the REST API, logs in through the real form, then
   drives the create-check flow on camera: checks list → **New check** → HTTP
   type → name + target URL → interval → regions → save → check detail page.
   Named still frames are written as it goes. The org is emptied afterwards.
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
