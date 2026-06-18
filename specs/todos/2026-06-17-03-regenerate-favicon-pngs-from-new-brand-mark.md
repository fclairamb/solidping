# Regenerate the favicon PNG set from the new brand mark so the new logo shows everywhere

## Context

The brand mark was recently redesigned and re-vectorized to match the
solidping.io logo:

- `21cbd847` — redesign `logo.svg` to match the solidping.io brand mark
- `21d6a1e5` — replace `logo.svg` with a high-fidelity vector trace (~97.4%
  fidelity, ~28 KB) of the reference PNG
- `825a9413` — sync `favicon.svg` to the new `res/logo.svg` (it still held the
  original placeholder shield)

`res/logo.svg` is the canonical new mark, and `res/logo.png` is its raster
source — **the SVG was traced from the PNG, so the two co-exist and both already
carry the current mark** (`res/logo.png`, mtime 06-16 22:32; `res/logo.svg`,
mtime 06-16 23:02). The in-app `<Logo>` component
([`web/dash0/src/components/ui/logo.tsx`](../../web/dash0/src/components/ui/logo.tsx),
[`web/status0/src/components/ui/logo.tsx`](../../web/status0/src/components/ui/logo.tsx))
renders `logo.svg`, so every in-app surface (sidebar, login, invite,
status-page header, design reference…) already shows the new mark.

**The gap is the raster favicon set.** `res/favicons/favicon-32.png`,
`favicon-192.png`, `favicon-512.png`, and `apple-touch-icon.png` were last
generated on **05-09** from the *old placeholder-shield* `logo.svg`, and were
never regenerated after the new mark landed on 06-16. They are byte-identical
across `res/favicons/`, both `web/{dash0,status0}/public/`, and both embedded
`server/internal/app/{dash0res,status0res}/` dirs — internally consistent, but
all still the old shield. So:

- Browser tabs that prefer the PNG icon (`<link rel="icon" type="image/png"
  sizes="32x32" href="favicon-32.png">`) show the old shield.
- iOS home-screen / Safari (`apple-touch-icon.png`) shows the old shield.
- PWA install icons (`manifest.webmanifest` → `favicon-192.png`,
  `favicon-512.png`) show the old shield.

Only the **SVG** favicon (`favicon.svg`, preferred by modern Chromium/Firefox)
shows the new mark, which is why it looks fixed in most dev browsers but is not
actually fixed everywhere.

Root cause: the favicon PNG set is a **committed binary artifact** generated
on-demand by `make build-favicons`, and the top-level `make build` target
(`sync-brand-assets build-dash copy-dash build-dash0 copy-dash0 …`) deliberately
does **not** run `build-favicons`. When the logo changed, nobody re-ran it.

## Decision

Regenerate the favicon PNG set from the new `res/logo.svg`, then propagate the
regenerated PNGs into both `public/` dirs and re-embed them into the server
resource dirs — using the existing `make build-favicons` / `make
sync-brand-assets` / `make copy-*` pipeline. No new tooling, no new wiring; the
`index.html` and `manifest.webmanifest` references are already correct and just
need the bytes behind them refreshed.

Per the asset model, **`logo.png` and `logo.svg` co-exist and both already hold
the new mark** — do **not** regenerate `logo.png` from the SVG, replace it, or
delete it. This change touches only the favicon/touch PNGs that are still the
old shield.

## Goals

- `res/favicons/{favicon-32,favicon-192,favicon-512,apple-touch-icon}.png` are
  regenerated from the current `res/logo.svg` and visibly show the new brand
  mark (not the old shield).
- The regenerated PNGs are propagated to `web/dash0/public/`,
  `web/status0/public/`, and the embedded `server/internal/app/dash0res/` +
  `server/internal/app/status0res/` so the new mark ships in the Go binary too.
- The new mark appears in every icon context: SVG favicon (already), PNG
  favicon, apple-touch-icon (iOS), and PWA manifest icons — in **both** dash0
  and status0.
- `logo.svg`, `favicon.svg`, and `logo.png` are unchanged (already the new
  mark); no churn there beyond what `sync-brand-assets` re-copies identically.

## Out of scope

- Touching the `<Logo>` component or any in-app rendering — it already sources
  `logo.svg` and is correct.
- Regenerating, replacing, or deleting `res/logo.png` / `res/logo.svg` /
  `res/favicon.svg` — they already carry the new mark and co-exist by design.
- `res/logo_256.png` (mtime Feb 14, old): it is **not referenced** by any
  `index.html`, `manifest.webmanifest`, `sync-brand-assets`, or app code, so it
  does not affect any rendered surface. Leave it alone (optionally note it as
  dead in a follow-up).
- Adopting `res/logo-gem.svg` (the lower-fidelity Gemini "comparison alternative"
  from `9ece2303`) — it is explicitly not the canonical mark.
- Changing `index.html` link tags, `manifest.webmanifest` entries, theme colors,
  or favicon sizes — the wiring is already correct.

## Implementation

All steps use existing Makefile targets. Render tooling is present locally
(`rsvg-convert` 2.62.1 preferred; ImageMagick `magick` as fallback) — see
[`scripts/build-favicons.sh`](../../scripts/build-favicons.sh).

### 1. Regenerate the favicon PNG set

```bash
make build-favicons
```

Runs [`scripts/build-favicons.sh`](../../scripts/build-favicons.sh), which
renders `res/logo.svg` → `res/favicons/favicon-{32,192,512}.png` and
`apple-touch-icon.png` (180×180). After this, `git status` should show those
four PNGs as modified (their sha must change from the old-shield versions:
`favicon-32` was `959ceeb1…`, `favicon-192` `38e75cb0…`, `favicon-512`
`ffb98927…`).

### 2. Propagate into both public dirs

```bash
make sync-brand-assets
```

Copies `res/logo.svg`, `res/logo.svg`→`favicon.svg`, `res/logo.png`, and
`res/favicons/*.png` into `web/dash0/public/` and `web/status0/public/`
([Makefile `sync-brand-assets`](../../Makefile)). The `logo.svg` / `favicon.svg`
/ `logo.png` copies are byte-identical no-ops; only the four PNGs change.

### 3. Re-embed into the Go server resources

The favicon PNGs are embedded into the binary via `go:embed` from
`server/internal/app/{dash0res,status0res}/`. Refresh them by rebuilding the
front-ends (which bundle `public/` into `dist/`) and re-copying:

```bash
make build-dash0 copy-dash0 build-status0 copy-status0
```

(or simply `make build`, which chains `sync-brand-assets` → builds → `copy-*`).
After this, `server/internal/app/dash0res/favicon-*.png` and the `status0res`
equivalents match the regenerated `res/favicons/*.png`.

### 4. Commit the regenerated binary assets

Commit the four regenerated PNGs in `res/favicons/`, both `public/` dirs, and
both `*res/` dirs. These are committed artifacts, so they must land in git for
the new mark to ship.

### (Optional) Prevent recurrence

Consider a lightweight guard so the favicon PNGs can't silently drift from
`logo.svg` again — e.g. a `make check-favicons` (or CI step) that re-renders to
a temp dir and `diff`s against the committed `res/favicons/*.png`, failing if
they differ. **Do not** simply add `build-favicons` as a hard dependency of
`make build`: that would make every build (including CI) require
`rsvg-convert`/`magick`, which the pipeline deliberately avoids by keeping the
PNGs as committed artifacts. Treat this as a follow-up, not part of the core
fix.

## Verification

1. **Bytes changed.** `git status` shows `res/favicons/{favicon-32,favicon-192,
   favicon-512,apple-touch-icon}.png` and their `public/` + `*res/` copies as
   modified; the new shas match a fresh render of `logo.svg` and differ from the
   old-shield shas above.
2. **PNG matches SVG.** Render the new mark at 32 px and confirm it matches the
   regenerated favicon, e.g.
   `rsvg-convert -w 32 -h 32 res/logo.svg -o /tmp/check.png` then compare
   `/tmp/check.png` with `res/favicons/favicon-32.png` (visually or via
   `magick compare -metric AE`); they should match, and both should differ from
   the pre-change committed PNG.
3. **dash0 in the browser.** With the app running, hard-refresh and confirm the
   tab favicon is the new mark; open the PWA install prompt / `manifest.webmanifest`
   icons and confirm the new mark. Repeat for **status0** (public status page).
4. **iOS / apple-touch-icon.** Open `…/apple-touch-icon.png` directly and confirm
   it renders the new mark, not the shield.
5. **Embedded binary.** `make build` then run the server; the favicons served
   from the embedded `dash0res`/`status0res` (not the dev public dir) show the
   new mark.

## Tests

This is a binary-asset refresh; there is no behavioral unit test to add. The
existing dash0/status0 Playwright suites don't assert favicon pixels and don't
need to. If a regression guard is wanted, add the optional `check-favicons`
drift check described above rather than a Playwright test.

## Files referenced

- [`scripts/build-favicons.sh`](../../scripts/build-favicons.sh) — generates the
  favicon PNG set from `res/logo.svg` (the command behind `make build-favicons`).
- [`Makefile`](../../Makefile) — `build-favicons` (76-77), `sync-brand-assets`
  (62-74), `copy-dash0` (106-111), `copy-status0` (118-123), `build` (60).
- `res/favicons/{favicon-32,favicon-192,favicon-512,apple-touch-icon}.png` — the
  stale assets to regenerate.
- `web/dash0/public/`, `web/status0/public/`,
  `server/internal/app/dash0res/`, `server/internal/app/status0res/` —
  propagation/embed targets.
- [`web/dash0/index.html`](../../web/dash0/index.html) (5-8) and
  `web/status0/index.html` — favicon / apple-touch-icon / manifest link tags
  (already correct).
- `web/dash0/public/manifest.webmanifest`, `web/status0/public/manifest.webmanifest`
  — PWA icon entries pointing at `favicon-192.png` / `favicon-512.png` (already
  correct).
- `res/logo.svg` (new mark), `res/logo.png` (raster source — leave as-is).

## Implementation Plan

1. `make build-favicons` — regenerate `res/favicons/*.png` from the new
   `res/logo.svg`; confirm the four PNG shas changed.
2. `make sync-brand-assets` — copy regenerated PNGs into both `public/` dirs
   (the `logo.svg`/`favicon.svg`/`logo.png` copies are no-ops).
3. `make build-dash0 copy-dash0 build-status0 copy-status0` (or `make build`) —
   re-embed the PNGs into `server/internal/app/{dash0res,status0res}/`.
4. Verify per the steps above (sha change, PNG matches SVG, browser tab + PWA +
   apple-touch-icon show the new mark in dash0 and status0).
5. Commit the regenerated PNGs across `res/favicons/`, both `public/` dirs, and
   both `*res/` dirs. Leave `logo.svg`, `favicon.svg`, `logo.png`, and
   `logo_256.png` untouched.
6. (Optional follow-up) add a `check-favicons` drift guard; do **not** make
   `make build` hard-depend on `build-favicons`.
