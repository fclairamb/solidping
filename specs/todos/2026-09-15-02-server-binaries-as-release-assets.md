---
model: sonnet
effort: high
---

# Every release ships `sp` but not `solidping`: the Linux and Windows install guides point at binaries that 404, and Windows doesn't even compile

## Problem

The published install guides tell people to download the server binary from the
latest GitHub release:

- [`web/docs/docs/installation/linux.md:16`](web/docs/docs/installation/linux.md:16),
  [`:28`](web/docs/docs/installation/linux.md:28) and
  [`:222`](web/docs/docs/installation/linux.md:222) —
  `releases/latest/download/solidping-linux-amd64` / `solidping-linux-arm64`
- [`web/docs/docs/installation/windows.md:22`](web/docs/docs/installation/windows.md:22)
  and [`:215`](web/docs/docs/installation/windows.md:215) —
  `releases/latest/download/solidping-windows-amd64.exe`

None of those assets has ever been published. The latest release, `v0.28.2`
(2026-09-14), carries exactly five assets — `sp_0.28.2_{darwin,linux}_{amd64,arm64}.tar.gz`
and `sp_0.28.2_checksums.txt` — and all three documented URLs answered
**HTTP 404** on 2026-09-15. The pages went live with the docs site (#88) and have
promised the download ever since; the Windows page alone is 200+ lines including
an NSSM service walkthrough, for a file that does not exist.

What exists today, and why none of it closes the gap:

- The PR-side `build` job ([`.github/workflows/ci.yml:435`](.github/workflows/ci.yml:435))
  already produces an artifact literally named `solidping-linux-amd64`
  ([`ci.yml:483`](.github/workflows/ci.yml:483)) — but as a 7-day CI artifact,
  on PR/main pushes only (`if: !startsWith(github.ref, 'refs/tags/')`,
  [`ci.yml:439`](.github/workflows/ci.yml:439)), never attached to a release.
- The `docker` job ([`ci.yml:737`](.github/workflows/ci.yml:737)) builds a
  **single platform** (no `platforms:`; only the `sp` image at
  [`ci.yml:919`](.github/workflows/ci.yml:919) is multi-arch). So an arm64 host
  — Graviton, Raspberry Pi, Apple Silicon without Rosetta — has *no* way to run
  SolidPing from a release: no image, no binary.
- `cli-release` ([`ci.yml:796`](.github/workflows/ci.yml:796), spec
  2026-09-11-04) does the whole dance for `sp` — cross-compile with
  `CGO_ENABLED=0`, checksum, wait for release-please's release, hand-pushed-tag
  fallback, `gh release upload --clobber` — but only for `./cmd/sp`, never for
  the server package.
- **`windows/amd64` does not compile.** `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build .`
  (from `server/`) fails on five unix-only sites:
  - [`server/pkg/cli/apihelper/apihelper.go:357`](server/pkg/cli/apihelper/apihelper.go:357)
    and [`server/pkg/cli/auth.go:407`](server/pkg/cli/auth.go:407) —
    `term.ReadPassword(syscall.Stdin)` (`syscall.Stdin` is a `Handle` on Windows)
  - [`server/internal/db/postgres/embeddedpg/sweep.go:262`](server/internal/db/postgres/embeddedpg/sweep.go:262)
    and [`watchdog.go:44`](server/internal/db/postgres/embeddedpg/watchdog.go:44) —
    `syscall.Kill`
  - [`server/internal/db/postgres/embeddedpg/watchdog.go:80`](server/internal/db/postgres/embeddedpg/watchdog.go:80)
    — `syscall.SysProcAttr{Setsid: true}`

Why this is cheap now: the SQLite driver selection at
[`server/internal/db/sqlitedriver/driver_modernc.go:1`](server/internal/db/sqlitedriver/driver_modernc.go:1)
picks the pure-Go driver on `darwin/{amd64,arm64}`, `linux/{386,amd64,arm,arm64}`
and `windows/amd64` unless the `cgosqlite` tag is set, so `CGO_ENABLED=0`
cross-builds of the *server* work exactly like they do for `sp`. Verified
locally on 2026-09-15: `CGO_ENABLED=0 go build .` succeeds for `linux/amd64`,
`linux/arm64` (≈5.5 min cold) and `darwin/arm64`; only `windows/amd64` fails,
on the five sites above. The Dockerfile's "CGO is needed for SQLite" comments
([`Dockerfile:85`](Dockerfile:85), [`:109`](Dockerfile:109)) are stale — on
`linux/amd64` without `cgosqlite` the mattn driver
([`driver_mattn.go:1`](server/internal/db/sqlitedriver/driver_mattn.go:1)) is
not compiled at all.

## Proposal

### 1. A tag-triggered `server-release` job next to `cli-release`

In [`.github/workflows/ci.yml`](.github/workflows/ci.yml), add
`server-release` ("Publish the server binaries"), `if: startsWith(github.ref, 'refs/tags/')`,
with **no `needs`** — `cli-release` and `docker` keep running in parallel, and
`deploy` keeps `needs: [docker]` so a failed asset upload never blocks a deploy.

1. **Build the three web dists in the job.** The `dash0`, `status0` and `docs`
   jobs are skipped on tags ([`ci.yml:295`](.github/workflows/ci.yml:295),
   [`:348`](.github/workflows/ci.yml:348), [`:401`](.github/workflows/ci.yml:401)),
   so their artifacts don't exist on the tag run. Reuse their install+build
   steps (`oven-sh/setup-bun`, `actions/setup-node`, `bun install`,
   `bun run build`) — no lint/unit steps, the tag commit already passed them on
   `main` — and drop the outputs where the `build` job does
   ([`ci.yml:448-462`](.github/workflows/ci.yml:448)):
   `server/internal/app/{dash0res,status0res,docsres}`. The docs build reads
   `server/internal/app/openapi/openapi.yaml` and the root `CHANGELOG.md` by
   relative path; both are in a normal checkout.
2. **Cross-compile the server package** (`.` in `server/`, not `./cmd/sp`) for
   `linux/amd64`, `linux/arm64`, `windows/amd64`, `darwin/amd64`, `darwin/arm64`
   with `CGO_ENABLED=0` and
   `-ldflags "-s -w -X …version.Version=… -X …version.Commit=… -X …version.GitTime=…"`,
   `VERSION` taken from the tag (`${GITHUB_REF#refs/tags/v}`) exactly as
   `cli-release` does ([`ci.yml:824-826`](.github/workflows/ci.yml:824)).
3. **Name the assets what the docs already promise** — fixed, version-free
   names, bare binaries:
   `solidping-linux-amd64`, `solidping-linux-arm64`, `solidping-windows-amd64.exe`,
   `solidping-darwin-amd64`, `solidping-darwin-arm64`, plus
   `solidping-checksums.txt` (`sha256sum` over the five, so
   `sha256sum -c --ignore-missing` works after downloading any one of them).
4. **Attach them** with the same wait-for-release loop and hand-pushed-tag
   fallback as `cli-release` ([`ci.yml:838-857`](.github/workflows/ci.yml:838)),
   then `gh release upload … --clobber`. Two jobs now share that fallback, so
   make `gh release create` tolerant of "already exists" (re-`view` after a
   failed create) — or factor the loop into a composite action under
   `.github/actions/` used by both jobs. Either is fine.

### 2. Make `windows/amd64` compile

- Both password prompts: `term.ReadPassword(int(os.Stdin.Fd()))` — the
  portable form.
- `embeddedpg`: move `syscall.Kill` and the `Setsid` attribute behind two
  helpers in `proc_unix.go` (`//go:build !windows`) and `proc_windows.go`.
  **Decision:** the Windows side may be honest stubs, with the
  `postgres-embedded` DB type refusing to start on Windows with a clear error
  (the switch is in [`server/main.go:539`](server/main.go:539) /
  [`:569`](server/main.go:569)). The Windows page documents only SQLite and
  external PostgreSQL ([`windows.md:27-41`](web/docs/docs/installation/windows.md:27)),
  so nothing documented is lost. A real port of the watchdog
  (`CREATE_NEW_PROCESS_GROUP` + `os.Process.Kill`) is welcome but not required.
- **Guard it on the PR side.** Extend the PR-side `build` job to also compile
  the four non-linux/amd64 targets to `/dev/null` with the *same* command the
  tag job uses (`CGO_ENABLED=0`, `-s -w`, ldflags). This is the only place the
  tag-only path gets exercised before a tag exists — a broken cross-build must
  fail the PR, not the release. Measure the added minutes; if it's more than a
  few, a matrix over the targets keeps wall-clock flat.

### 3. Docs

- [`linux.md`](web/docs/docs/installation/linux.md): after the download, a
  checksum step (`curl -L -o solidping-checksums.txt …/latest/download/solidping-checksums.txt && sha256sum -c --ignore-missing solidping-checksums.txt`),
  and a short **macOS** note: same URLs with `darwin-arm64` / `darwin-amd64`;
  the binary is unsigned, so Gatekeeper may need
  `xattr -d com.apple.quarantine solidping`. Keep it a subsection — a dedicated
  macOS page is out of scope.
- [`windows.md`](web/docs/docs/installation/windows.md): checksum via
  `Get-FileHash`, and one line saying `postgres-embedded` is not available on
  Windows if that is what §2 shipped.
- [`README.md:47`](README.md:47) "Self-host" row: optionally mention the bare
  binary next to the `docker run`.
- Changelog entry through the conventional commit (`feat(release): …`), per
  [`wiki/conventions/changelog.md`](wiki/conventions/changelog.md). The user-facing
  message: every release now ships the server for Linux, macOS and Windows, and
  the install guides' download links work.

Leave the Dockerfile's stale CGO comments alone in this spec — changing the
image build is a different change with its own blast radius.

## Decisions (and what was rejected)

- **Fixed names, bare binaries — not `solidping_${VERSION}_${os}_${arch}.tar.gz`
  like `sp`.** The docs' URLs are the published contract;
  `releases/latest/download/<fixed-name>` is the version-agnostic URL that
  scripts, Ansible and the docs' own `curl -L -o solidping … && chmod +x` lines
  rely on; Windows users get a runnable `.exe` with no tar step; `--clobber`
  keeps re-runs idempotent. The cost is no compression: the local unstripped
  build is 247 MB (gzip -1 → 98 MB), of which the embedded docs are 49 MB,
  dash0 4.7 MB, status0 1.6 MB — so `-s -w` is mandatory, and expect roughly
  100+ MB per stripped asset. GitHub allows 2 GiB per asset; a self-hosted
  server is downloaded once. `sp` keeps its versioned archives — its audience
  (CI jobs) pins versions.
- **Five platforms.** The three the docs promise, plus both darwin arches
  because the pure-Go driver covers them and the same loop builds them for
  free. No `linux/386` or `linux/arm` (v7) — nobody asked; the driver tag list
  allows adding them later.
- **A separate job, not an extension of `cli-release`.** `sp`'s five archives
  should not wait behind three web builds, and the two uploads fail
  independently.
- **`-s -w`** like `sp`: Go keeps `pclntab`, so panics still print symbolic
  stack traces and pprof still works.

## Verification

- PR side: the extended `build` job compiles all five targets; `make build`,
  `make lint`, `make test` green. Locally, run the job's loop, check `file`
  reports the right OS/arch for each output, and start the host-native binary
  (`./solidping serve` against SQLite) and hit `/api/mgmt/version` to confirm
  the ldflags-injected version/commit.
- **The tag path cannot run on the PR.** Acceptance is on the first release
  after merge: `gh release view <tag> --json assets` lists six new assets;
  `curl -sIL …/releases/latest/download/solidping-linux-amd64` answers 200;
  `sha256sum -c` passes; `docker` and `deploy` unaffected. Whoever cuts that
  release (`/merge-and-release`) checks this — note it in the PR description.
- The implementation itself pushes **no tag** and creates **no release**.

## Out of scope

- A multi-arch main Docker image (`linux/arm64`) — separate spec; the arm64
  binary covers the immediate gap.
- Windows assets for `sp` — the `apihelper.go` fix unlocks it; adding
  `windows/amd64` to `cli-release`'s loop is a one-line follow-up.
- A Homebrew tap, an `install.sh`, macOS signing/notarization.
