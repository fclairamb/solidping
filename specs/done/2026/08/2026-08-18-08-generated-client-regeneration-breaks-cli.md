---
model: sonnet
effort: medium
---

# Regenerating the Go client from the current OpenAPI spec breaks `pkg/cli`

## Problem

`server/pkg/client/client_generated.go` is regenerated **once per batch/release**,
not per commit — every commit that has ever touched it is a squash-merge of a
batch PR (`git log -- server/pkg/client/client_generated.go` shows only
`(#217)`, `(#212)`, `(#209)`, … titles). That cadence is fine on its own, but it
means `openapi.yaml` and the committed client drift apart for the length of a
batch, and the drift is only discovered by whoever runs the regeneration.

Right now that regeneration does not compile. Running
`go generate ./pkg/client/...` on the current tree succeeds, and the resulting
`go build ./...` then fails in **`server/pkg/cli`**:

```
pkg/cli/incidents.go:155:71: not enough arguments in call to apiClient.GetIncidentWithResponse
	have (context.Context, string, uuid.UUID)
	want (context.Context, client.OrgPath, client.IncidentUidPath, *client.GetIncidentParams, ...client.RequestEditorFn)
pkg/cli/statuspages.go:269:21: cannot use slug (variable of type string) as *string value in struct literal
pkg/cli/statuspages.go:517:13: cannot use slug (variable of type string) as *string value in struct literal
```

Two independent causes:

1. **`GetIncidentWithResponse` gained a params argument.** Batch
   `batch/2026-08-17` added a `with` query parameter to the `getIncident`
   operation in `server/internal/app/openapi/openapi.yaml` (spec
   `2026-08-18-01-incidents-list-n-plus-one-enrichment`, which made
   `with=members` load-bearing on that endpoint). oapi-codegen turns any
   operation with query parameters into a `*XxxParams` argument, so the call
   site needs a `nil` (or a real params value) it does not currently pass.
2. **`statuspages` slug fields are `*string`.** Pre-existing drift, unrelated to
   that batch — the committed client predates whatever schema change made those
   optional.

Neither is caught by CI, because CI builds the **committed** generated file, not
a freshly generated one. The failure is deferred to release time and lands on
whoever is cutting the release rather than on whoever caused it.

## Proposal

### 1. Fix the two call sites

Regenerate the client and repair `server/pkg/cli`:

- `pkg/cli/incidents.go:155` — pass the new params argument. `nil` is correct
  unless the CLI should expose `with` (it currently does not ask for members or
  check details, so `nil` preserves today's behaviour — but check what the
  command prints before deciding).
- `pkg/cli/statuspages.go:269` and `:517` — take the address of `slug`, or use
  whatever pointer helper the surrounding code already uses. Look for an
  existing helper rather than adding a new one.

Commit the regenerated `client_generated.go` alongside the fixes so the tree is
self-consistent.

### 2. Make the drift fail fast, in CI

The real defect is that nothing notices. A regeneration that does not compile
should break the build of the PR that caused it, not the release.

Add a CI check that regenerates the client into a scratch location and fails if
either (a) the result differs from the committed file, or (b) the tree does not
build against it. Prefer (b) at minimum — a pure diff check will be noisy if
codegen output is not byte-stable across tool versions, whereas "the
freshly-generated client still compiles" is exactly the invariant that broke
here.

If a per-PR regeneration is too slow or too flaky (oapi-codegen has been fragile
in this repo before — the tool itself failed to build in 2026-08 when go-yit
drifted past yaml-jsonpath and had to be pinned), a nightly or pre-release job
is an acceptable fallback, but say so explicitly in the workflow comment rather
than leaving the weaker check looking like the intended one.

### 3. Decide and document the regeneration cadence

`wiki/conventions/` should state when the client is regenerated and who owns it.
The current "once per batch, implicitly, by whoever notices" is what let this
drift accumulate silently.

## Verification

- `go generate ./pkg/client/...` followed by `go build ./...` succeeds on a
  clean tree.
- The new CI check fails on a deliberately introduced OpenAPI change that breaks
  a CLI call site, and passes once the call site is fixed (prove both directions
  — a check that only ever passes proves nothing).
- `pkg/cli`'s incident and status-page commands still behave as before; cover
  the `GetIncident` call site with a test if one does not already exist.

## Notes

- Found while auditing spec `2026-08-18-04-results-pagination-total-is-always-zero`:
  the implementer deliberately left `client_generated.go` unregenerated,
  correctly citing the once-per-batch cadence. Verifying that claim surfaced
  this. The decision not to regenerate in that spec was right — regenerating
  pulls in unrelated breakage that is not that spec's to fix.
