---
model: sonnet
effort: medium
---

# Release binaries are published uncompressed; ship only `.gz` (and `.zip` for Windows)

## Problem

Every tag publishes each executable twice or uncompressed:

- **`cli-release`** (`.github/workflows/ci.yml:913`) builds 5 bare `sp` binaries
  (`sp-{linux,darwin}-{amd64,arm64}`, `sp-windows-amd64.exe`), then adds a `gzip -9 -k`
  twin of each (`ci.yml:973-988`). So the release carries 10 `sp` files, and the
  Windows one ships as a `.exe.gz`, which Windows users cannot open without extra tools.
- **`server-release`** (`ci.yml:1087`) builds 5 bare `solidping-*` binaries and uploads
  them as-is (`ci.yml:1219`). No compressed variant at all, even though the server binary
  embeds dash0, status0 and the docs and is the largest file we publish.

Bare binaries are slow to download and clutter the release page. The `.gz` twins exist
only because nobody dared remove the bare files.

## Proposal

Publish every executable **compressed only**:

| Platform | `sp` asset | server asset |
|---|---|---|
| linux/amd64 | `sp-linux-amd64.gz` | `solidping-linux-amd64.gz` |
| linux/arm64 | `sp-linux-arm64.gz` | `solidping-linux-arm64.gz` |
| darwin/amd64 | `sp-darwin-amd64.gz` | `solidping-darwin-amd64.gz` |
| darwin/arm64 | `sp-darwin-arm64.gz` | `solidping-darwin-arm64.gz` |
| windows/amd64 | `sp-windows-amd64.zip` | `solidping-windows-amd64.zip` |

Names stay version-free so `releases/latest/download/<name>` remains a stable URL.
`check-config-schemas.tar.gz` and the `*-checksums.txt` files are not executables and stay
as they are.

### CI (`.github/workflows/ci.yml`)

1. **`cli-release`**: after the build loop, replace the `gzip -9 -k` step with:
   - non-Windows: `gzip -9 -n` (no `-k`, so the bare file is removed; `-n` drops the
     embedded name/mtime so the `.gz` is reproducible);
   - Windows: `zip -9 -j sp-windows-amd64.zip sp.exe` (build the Windows target to a
     temp `sp.exe` so the extracted file is directly runnable as `sp.exe`), then delete
     the bare `.exe`.
   - `sp-checksums.txt` is computed over the published `.gz`/`.zip` files.
   - Update the comment block at `ci.yml:973-979`, which currently argues the bare names
     are "the canonical, unpinned contract".
2. **`server-release`**: same treatment, inner Windows name `solidping.exe`.
   `solidping-checksums.txt` covers the `.gz`/`.zip` files. Rewrite the comment at
   `ci.yml:1145-1153` ("Bare binaries, not tarballs ... Windows users get a runnable .exe").
3. Add a guard at the end of each build step: fail the job if `dist/` still contains any
   bare `sp-*`/`solidping-*` file that is not `.gz`, `.zip` or `-checksums.txt`. The upload
   globs (`dist/sp-*`, `dist/solidping-*`) would otherwise silently republish a stray
   bare file.
4. Optionally extend the PR-time compile-only guard (`ci.yml:534`) to run the same
   packaging on the linux/amd64 build, so a broken `zip`/`gzip` invocation is caught before
   a tag exists (the release jobs only run on `refs/tags/`).

### Docs and downstream consumers (same change)

- `web/docs/docs/installation/linux.md:16,28,51,54,261`: `curl -L ... solidping-linux-amd64.gz | gunzip > solidping && chmod +x solidping`. Checksum section (`:38`) must say the sums are of the `.gz` files, verified before `gunzip`.
- `web/docs/docs/installation/windows.md:15,22,31-36,240`: download the `.zip`, `Expand-Archive`, then run `solidping.exe`. Update the checksum lookup string.
- `web/docs/docs/cli.md:17,28,34` and `wiki/features/config-as-code.md:101-110`: same, for `sp`.
- `wiki/distribution/yunohost.md:60-61`: the `solidping_ynh` autoupdate regexes
  `solidping-linux-amd64$` / `solidping-linux-arm64$` stop matching. Per that page, the
  `autoupdate.asset.*` patterns and `[resources.sources.main]` URLs in `solidping_ynh`
  must be bumped (to `\.gz$` plus the YunoHost source `format`/`extract` settings for a
  gzipped single file). Update the wiki page and list the external repo change in the PR
  description; it lives outside this repo.
- Check the `solidping-website` repo for install snippets pointing at the bare names and
  note them in the PR description if any.
- `CHANGELOG.md`: this is a **breaking change for anyone scripting
  `releases/latest/download/solidping-linux-amd64`** (those URLs 404 from the first release
  that ships this). Say so explicitly in the entry, with the new command. Older releases
  keep their bare assets.

### Overlap with `2026-09-24-10-sp-cli-asset-names-hyphen-docs`

That spec fixes `cli.md` / `config-as-code.md` pointing at underscore names
(`sp_linux_amd64`) that 404. This spec rewrites the same lines to the `.gz` names. Whichever
lands second must re-check those lines; if this one lands first, 24-10 reduces to verifying
nothing still says `sp_`.

**Status:** 24-10 has already landed (`specs/done/2026/09/`). It replaced every underscore
name with the hyphenated bare name (`sp-linux-amd64`, `sp-windows-amd64.exe`, …) in
`web/docs/docs/cli.md`, `wiki/features/config-as-code.md` and two CHANGELOG entries, and
kept `cli.md`'s "~32 MB bare / ~12 MB `.gz`" sizes (verified against the v0.32.1 release).
This spec lands second, so it must rewrite those now-hyphenated bare-name lines to the
`.gz`/`.zip` names. Leave the released CHANGELOG entries alone: they describe what those
releases actually shipped.

## Verification

- Run both build steps locally (or with `act`) for one tag-like `GITHUB_REF` and list
  `dist/`: exactly 5 `.gz`/`.zip` per binary plus the checksum file, no bare executable.
- `gunzip -c sp-linux-amd64.gz > sp && chmod +x sp && ./sp --version` prints the injected
  version. `unzip -l sp-windows-amd64.zip` shows a single `sp.exe` at the root.
- `sha256sum -c sp-checksums.txt` passes against the published files.
- After the first real release: `curl -sI .../releases/latest/download/solidping-linux-amd64.gz`
  returns 302 → 200, and the bare name returns 404.

## Open questions

- **Inner file name in the `.zip`**: proposed `sp.exe` / `solidping.exe` (runnable name
  after extraction, matches the docs' `-OutFile solidping.exe`). Alternative: keep
  `solidping-windows-amd64.exe`. Pick one and document it.
- **Transition period**: publish the bare names for one more release with a deprecation
  note, or cut over in one go? Default: cut over, since old releases keep working and
  the changelog calls it out.

## Resolved open questions

- **Inner file name in the `.zip`**: use the short runnable name — `sp.exe` inside
  `sp-windows-amd64.zip`, and `solidping.exe` inside `solidping-windows-amd64.zip`. It matches
  the docs' `-OutFile solidping.exe` and is what a user expects after unzipping; the platform
  and arch stay in the archive name. Document it in the install docs.
- **Transition period**: cut over in one go, as the spec's default says. Old releases keep
  their bare assets; the changelog calls out the change.
