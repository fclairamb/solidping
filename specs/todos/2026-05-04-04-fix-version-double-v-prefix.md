# Fix "Powered by SolidPing vv0.0.0" double-`v` prefix

## Context

Both dash0 and status0 display a literal `v` before the version string they fetch from `/api/mgmt/version`. When the build pipeline produces a version that already starts with `v` (the Dockerfile path — see below), the result is `vv0.0.0`. The two offending lines are:

- `web/status0/src/components/shared/status-page-view.tsx:252` — `{t("poweredBy")}{versionInfo ? ` v${versionInfo.version}` : ""}`
- `web/dash0/src/routes/orgs/$org/login.tsx:441` — `<span data-testid="login-version">v{versionData.version || "unknown"}</span>`

The build pipeline is itself inconsistent. `Makefile:9` strips `v` from `git describe`:
```
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo "dev")
```
…but `Dockerfile:102-104` passes `${VERSION}` through verbatim with no `sed`. So a `make build` produces `0.0.0` and `make docker-build` produces `v0.0.0` — two artifacts, two different wire values, and the frontend always tacks on another `v`.

## Scope

**In scope:**
- Pick one canonical wire format. Recommendation: **the API never returns a leading `v`**, the frontend never adds one. The literal prefix in the UI stays as `v` (so users still see `v0.0.0`), but it lives only in the JSX, and the wire value is unprefixed.
- Align the Dockerfile to strip `v` like the Makefile does.
- Or, equivalent: strip `v` once at the boundary in `server/internal/version/version.go` (defense in depth — even if a build accidentally leaks `v0.0.0`, the API normalises it).
- Fix the two frontend sites to keep showing `v` — the bug is the *double* `v`, not the prefix itself.
- Unit test on `server/internal/version/version.go` asserting `Get().Version` does not start with `v`.

**Out of scope:**
- Replacing `v0.0.0` with semver-only display.
- Changing where the version is sourced (still `git describe`).

## Approach

### 1. Normalise at the version source

`server/internal/version/version.go` — in `Get()` (around line 39), strip a leading `v` from the raw `Version` variable before returning:

```go
func Get() Info {
    v := strings.TrimPrefix(Version, "v")
    return Info{Version: v, Commit: Commit, GitTime: GitTime}
}
```

This makes the wire format independent of how the build was invoked. Add a unit test in `server/internal/version/version_test.go`:

```go
func TestGet_StripsLeadingV(t *testing.T) {
    Version = "v1.2.3"
    if got := Get().Version; got != "1.2.3" {
        t.Fatalf("want 1.2.3, got %q", got)
    }
}
```

(Use a `t.Setenv` / package-level reset pattern if `Version` is touched by other tests.)

### 2. Align the Dockerfile (defense in depth)

`Dockerfile:102-104` — apply the same `sed` as the Makefile to keep build artifacts uniform even before the runtime stripping. Optional but cheap.

### 3. Frontend stays unchanged in spirit

The two JSX lines already render `v` as a literal prefix. After step 1, they correctly produce `v0.0.0`. **No frontend change is required** — but it's worth a sanity comment in each file pointing to this spec, so a future contributor doesn't "fix" the literal `v` thinking they're undoing the bug.

(If the existing strings are localized via i18n, ensure the literal `v` lives in the source code, not in the translation file — translators shouldn't be expected to know about it.)

### 4. Audit other version sites

Grep the repo for `version` and confirm no other display path adds another `v`:
- `web/dash0/src/components/feedback/useFeedback.ts:136` (uses `VITE_APP_VERSION`)
- `/api/mgmt/version` consumers
- The CLI `solidping --version`

## Verification

1. `make build && ./solidping --version` → prints something like `0.0.0` (no leading `v`).
2. `make docker-build && docker run … solidping --version` → same.
3. `curl -s http://localhost:4000/api/mgmt/version | jq -r .version` → no leading `v`.
4. Visit `https://solidping.k8xp.com/dash0/` login page → footer shows exactly one `v`: `v0.0.0`.
5. Visit a public status page → footer "Powered by SolidPing v0.0.0" shows exactly one `v`.
6. `make test` passes.
