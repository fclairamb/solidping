# Generated Go client (`server/pkg/client`)

`server/pkg/client/client_generated.go` is produced by oapi-codegen from
`server/internal/app/openapi/openapi.yaml` (config: `server/pkg/client/oapi-codegen.yaml`,
trigger: `server/pkg/client/generate.go`'s `//go:generate` directive, also wired into
`make generate`).

## Regeneration cadence: once per batch/release, not per commit

The generated client is regenerated and committed **once per batch or release**, by
whoever is cutting it — not on every commit that touches `openapi.yaml`. That means
`openapi.yaml` and the committed `client_generated.go` are allowed to drift apart for
the length of a batch. A spec that only changes the OpenAPI spec should **not** also
regenerate and commit the client as a side effect — regenerating pulls in every other
unregenerated change since the last batch too, which is not that spec's to fix or
review.

**Owner:** whoever cuts the batch/release PR regenerates the client as part of that PR,
by running:

```bash
cd server && go generate ./pkg/client/...
```

and committing the result alongside any call-site fixes it requires.

## Why this is still safe: CI checks the invariant that matters

Letting the client drift is fine on its own, but a regeneration that doesn't compile
is a real defect — one introduced by whichever PR changed `openapi.yaml` in an
incompatible way (e.g. adding a required query parameter to an operation a hand-written
caller in `server/pkg/cli` uses). Deferring discovery of that to release time lands the
failure on the release cutter instead of the PR author who caused it.

To catch this immediately, CI's `backend-lint` job (`.github/workflows/ci.yml`)
regenerates the client from the **current** `openapi.yaml` on every PR — into the
checked-out working tree, never committed — and then builds and tests against it, same
as it would against a real regeneration. If the regeneration doesn't compile, that job
fails on the PR that broke it.

This is deliberately a **build check, not a byte-diff check** against the committed
file: since the committed client is allowed to lag `openapi.yaml` mid-batch by design,
a diff check would false-fail on every ordinary commit that touches the spec without
regenerating. "The freshly generated client still compiles" is the invariant that
actually matters, and it doesn't have that false-positive problem.

## oapi-codegen fragility

The codegen tool itself has broken before (2026-08): a transitive dependency
(go-yit, via yaml-jsonpath) drifted past what oapi-codegen required, so
`go generate ./pkg/client/...` failed before it even reached `openapi.yaml`. If that
happens again, check `server/go.mod` for the go-yit pin before assuming the OpenAPI
spec itself is at fault.

## Fixing a call site after regeneration

`server/pkg/cli` is the main hand-written consumer of the generated client (the
dashboard's own API calls are generated separately for `web/dash0`). When
regenerating changes a method signature — e.g. an operation gains a query
parameter and oapi-codegen adds a `*XxxParams` argument — fix call sites by:

- Passing `nil` when the CLI doesn't need the new behavior (check what the command
  prints/does first — `nil` must actually preserve current behavior).
- Reusing an existing pointer helper (`optString`, `optInt`, `optUUID` in
  `pkg/cli/statuspages.go`, or just `&localVar` for an already-validated required
  value) rather than adding a new one.
