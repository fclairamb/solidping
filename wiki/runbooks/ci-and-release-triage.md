# Reading CI and releases without fooling yourself

Every item here is a way GitHub Actions, the linters or release-please reported
something that was not true. Read this before concluding "flake", "green" or
"released".

## CI states that lie

- **`skipping` is not healthy.** E2E and Build Binary are skipped when an upstream job
  (Dash0 Build, Backend Lint & Test) fails. On a long-lived branch with one red job, E2E
  regressions pile up unseen, then all land at once when the upstream job goes green,
  and look like the fix broke them. Compare with `main`'s runs to know whether E2E is
  met or only deferred.
- **A conflicting PR creates no runs at all.** With `mergeStateStatus: DIRTY`, GitHub
  cannot build the merge ref, so a new push gets zero workflow runs. `gh pr checks`
  says `no checks reported` and exits non-zero, which a naive wait loop reads as
  "pending". Confirm with
  `gh api "repos/fclairamb/solidping/actions/runs?head_sha=$SHA" --jq .total_count`
  (0) and `gh pr view <n> --json mergeStateStatus`. `BLOCKED` only means checks are
  pending. Fix by merging `origin/main`; for `go.sum` conflicts take the merged
  `go.mod` and run `go mod tidy` rather than hand-resolving hashes. `main` moves during
  a long batch: re-check before the final push, not only at the start.
- **Run states can flap.** A job re-queued after a runner loss goes
  `completed/failure` back to `in_progress`. Require two consecutive stable reads, and
  prefer the REST jobs API over `gh pr checks` buckets.
- **The E2E "server was DOWN" banner is script text.** If the job died at setup
  (for example `failed to bind host port ... 55432 ... address already in use`), the
  unconditional banner follows. Look for the setup error before believing it.
- **`gh run view --log-failed` refuses while any job still runs.** Use
  `gh api repos/fclairamb/solidping/check-runs/<id>/annotations` for lint findings
  right away.
- **Playwright browser install used to hang** in the `--with-deps` apt phase until the
  6 h job timeout. Each attempt is now wrapped in `timeout 90` inside a retry loop, and
  the job has `timeout-minutes`. A re-run alone was not a reliable fix.

## Local lint must match CI's linter

`make lint-back` runs whatever `golangci-lint` is first on `PATH`. CI installs the
version pinned in `.github/workflows/ci.yml` with `install-mode: goinstall`, so its
linter is compiled with CI's Go, and its embedded `go/format` is that Go's. A green
local lint is only a predictor when both match.

- Check `golangci-lint --version` (it prints the Go it was built with) against
  `ci.yml`. After a Go upgrade, re-`go install` the linter: the version number alone
  is not enough. A second, older install (for example Homebrew's) can shadow it if
  `PATH` order changes.
- Exact replica, without touching the global binary:
  ```bash
  GOTOOLCHAIN=go<ci-go> GOBIN=<scratch>/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@<ci-version>
  cd server && GOTOOLCHAIN=go<ci-go> <scratch>/bin/golangci-lint run ./...
  ```
- Linter versions disagree in ways no directive satisfies. Seen: one `unparam` version
  fires on an always-nil `error` return and the next does not, so the `//nolint:unparam`
  waiver becomes an unused directive that `nolintlint` fails. A local `unparam`
  complaint CI does not raise means the local linter is stale.
- gofmt and gofumpt can deadlock: two composite literals returned inline
  (`return []string{...}, []string{...}`) get de-indented by one and re-indented by the
  other. Fix the code (assign to locals, `return a, b`), never the config.
- `golangci-lint fmt --diff` skips `_test.go` files while `run` includes them. Reproduce
  formatter findings with `run`. The formatter name `run` reports can be unrelated to
  the real diff: get the diff from the tool (`gofumpt -d`,
  `$(GOTOOLCHAIN=go<ci-go> go env GOROOT)/bin/gofmt -d`; `GOTOOLCHAIN` does not redirect
  the `gofmt` on `PATH`).
- Fix lint findings in the code. Never relax `.golangci.yml` to get green. A test-only
  exclusion is acceptable only with a `path: _test\.go` filter, after showing the code
  fix was tried.

## Release flow

1. PRs are squash-merged with the PR title as the commit subject.
2. release-please (`.github/workflows/release-please.yml`, `release-type: simple`)
   opens or updates `chore(release): vX.Y.Z` on `release-please--branches--main`.
   `feat` bumps the minor, `fix` the patch, hidden types (`chore`, `docs`, `ci`,
   `refactor`, `test`) ship without a changelog line or a bump.
3. Expand the changelog in the release PR (see
   [changelog conventions](../conventions/changelog.md)). Edit from a separate
   worktree; the local `release-please--branches--main` ref is usually stale, so
   `git checkout -B <branch> origin/<branch>` there.
4. Merging the release PR makes release-please create the tag and GitHub release.
5. **Build and deploy are tag-triggered.** On the release commit's `main` run the
   Docker and deploy jobs show "skipped". The real build is the `vX.Y.Z` tag run.
   That deploy only updates the main dev API Deployment; every other deployment is
   bumped by hand.

## release-please fails silently

The workflow goes green in every case below. The tell is the release PR's
`headRefOid` not moving after a merge, or the release PR still labelled
`autorelease: pending` instead of `autorelease: tagged` after its own merge.

- **Unparseable PR title.** Double quotes in the title, or no conventional type at all,
  drop the commit from both the changelog and the version computation. One such title
  once made 147 commits vanish. `.github/workflows/pr-title.yml` validates titles now.
  The run log says `commit could not be parsed`.
- **Hand-edit then a fast auto-merge.** Pushing a changelog commit to the release
  branch and merging within ~90 s, before release-please re-runs, twice produced no tag.
  The `Detect release drift` step catches it. Wait for one release-please run after
  editing, or plan to tag by hand.

Recovery when the tag is missing:

```bash
FULL=$(gh pr view <releasePR> --json mergeCommit --jq .mergeCommit.oid)   # full 40-char sha, a short one is rejected
git show origin/main:CHANGELOG.md | awk '/^## \[X.Y.Z\]/{f=1;next} /^## \[/{f=0} f' > notes.md
gh release create vX.Y.Z --target "$FULL" --title vX.Y.Z --notes-file notes.md
gh pr edit <releasePR> --remove-label "autorelease: pending" --add-label "autorelease: tagged"
gh pr list --state open    # close any chore(release) PR with a LOWER version, never merge it
```

That failing run often opens a bogus release PR for a lower version; merging it
downgrades the manifest and rewrites `CHANGELOG.md`. A tag created with a user token
does trigger the tag-gated build and deploy.

When the version itself is wrong (features shipped under a patch), merge a PR whose
squash body carries a `Release-As: X.Y.Z` footer (pass `--subject`/`--body` to
`gh pr merge --squash` so the footer survives), then write the changelog section by
hand on the release branch.

## Squash merges make every branch look unmerged

Local branches never have merge ancestry with `main`, so `git branch --no-merged`
lists hundreds of shipped branches, and a three-dot `git diff origin/main...<branch>`
still shows their full diff. To find really unshipped work, compare tips:
`git diff --stat origin/main <branch>` (two dots), or
`git merge-tree --write-tree origin/main <branch>` and compare with
`origin/main^{tree}`. Never bulk-merge branches from `--no-merged`, and never hand-merge
`release-please--branches--main`.
