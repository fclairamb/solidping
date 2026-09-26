---
model: sonnet
effort: low
---

# The `sp` CLI install docs point at underscore asset names that 404

## Problem

The release workflow publishes the `sp` CLI under hyphenated, version-free names
(`.github/workflows/ci.yml:990` builds `sp-checksums.txt` from `sp-*`):

- `sp-linux-amd64`, `sp-linux-arm64`, `sp-darwin-amd64`, `sp-darwin-arm64`, `sp-windows-amd64.exe`
- their `.gz` twins (`sp-linux-amd64.gz`, …)
- `sp-checksums.txt`

Several docs use underscore names instead. Verified:
`https://github.com/fclairamb/solidping/releases/latest/download/sp_linux_amd64` returns **404**,
`.../sp-linux-amd64` returns **200**. So every copy-pasted install snippet fails.

Wrong names, found with `git grep -n 'sp_\(linux\|darwin\|windows\)' -- ':!specs/done'`:

- `web/docs/docs/cli.md:17` — `latest/download/sp_linux_amd64`
- `web/docs/docs/cli.md:28` — pinned `download/v${VERSION}/sp_linux_amd64`
- `web/docs/docs/cli.md:31` — `sp_windows_amd64.exe`
- `web/docs/docs/cli.md:34` — PowerShell `latest/download/sp_windows_amd64.exe`
- `web/docs/docs/cli.md:37` — `sp_linux_amd64.gz`, and the `gunzip sp_linux_amd64.gz && chmod +x sp_linux_amd64` line
- `wiki/features/config-as-code.md:109` — CI snippet `latest/download/sp_linux_amd64` (the prose just above, line ~102, already uses the right hyphenated names)
- `CHANGELOG.md:53` — 0.31.1 entry, `releases/latest/download/sp_linux_amd64`
- `CHANGELOG.md:36` — 0.32.0 "gzip twin" entry, `sp_linux_amd64.gz` (twice)

The grep misses one more: the platform hint comment at `web/docs/docs/cli.md:15`
(`# Pick your platform: darwin_amd64, darwin_arm64, linux_amd64, linux_arm64, windows_amd64`)
also uses underscores.

The GitHub release notes for v0.31.1 and v0.32.0 already use the correct names; only the repo copies are wrong.

## Proposal

1. Replace every underscore asset name above with its hyphenated form
   (`sp_linux_amd64` → `sp-linux-amd64`, `sp_windows_amd64.exe` → `sp-windows-amd64.exe`,
   `sp_linux_amd64.gz` → `sp-linux-amd64.gz`). Fix the `cli.md:15` platform list too
   (`darwin-amd64, darwin-arm64, linux-amd64, linux-arm64, windows-amd64`).
2. Leave `specs/done/` alone (historical record).
3. Check each snippet still works end to end, not just that the name changed:
   - `curl -sSL -o sp .../latest/download/sp-linux-amd64` returns a binary (HTTP 200, not an HTML 404 page saved as `sp`).
   - The pinned-version URL resolves for a real tag (e.g. `v0.32.0`).
   - The `.gz` line: `gunzip sp-linux-amd64.gz && chmod +x sp-linux-amd64` — confirm the file name after `gunzip` matches what `chmod` targets, and that the result runs (`./sp-linux-amd64 --version`).
   - The PowerShell URL resolves (`curl -sI` on the `.exe` URL is enough).
4. After the fix, `git grep -n 'sp_\(linux\|darwin\|windows\)' -- ':!specs/done'` returns nothing.
5. Branch `fix/sp-cli-asset-names`, PR with a conventional-commit title such as
   `docs(cli): use the hyphenated sp release asset names`.

## Open questions

- `cli.md:37` says the bare files are ~32 MB and the `.gz` twins ~12 MB, while the 0.32.0
  CHANGELOG entry says ~99 MB → ~31 MB. Check the real sizes on the latest release and fix
  whichever is wrong while in the file.
- Editing already-released CHANGELOG entries: fine here since the published release notes
  are already correct and `/docs/changelog` is generated from this file, so the site would
  otherwise keep serving the 404 URL.

## Resolved open questions

- **Which asset size is correct, `cli.md:37`'s ~32 MB/~12 MB or the CHANGELOG's ~99 MB/~31 MB?**
  Decision: don't guess between the two conflicting numbers — pull the real sizes from the
  latest GitHub release (e.g. `curl -sI` against the `.../latest/download/...` asset URLs, or
  the GitHub releases API) before editing either spot, and use that measured value as the
  single source of truth for both `cli.md` and the CHANGELOG entry if it also needs
  correcting.
