---
model: sonnet
effort: medium
---

# `getCheck` accepts `?with=` but never declares it, so the embeds are missing from the docs and the generated client

## Problem

`GetCheck` parses the `with` query parameter and supports exactly two
components — `last_result` and `last_status_change`
([`handler.go:344-357`](../../server/internal/handlers/checks/handler.go)):

```go
withParam := req.URL.Query().Get("with")
if withParam != "" {
	for _, part := range strings.Split(withParam, ",") {
		switch strings.TrimSpace(part) {
		case "last_result":
			opts.IncludeLastResult = true
		case "last_status_change":
			opts.IncludeLastStatusChange = true
		}
	}
}
```

That is the **same set** the list endpoint supports
([`handler.go:171-173`](../../server/internal/handlers/checks/handler.go)) — verified
by comparing the two switch statements, not assumed.

But the `getCheck` operation in
[`openapi.yaml:1091-1105`](../../server/internal/app/openapi/openapi.yaml) declares
only its two path parameters:

```yaml
      parameters:
        - $ref: "#/components/parameters/OrgPath"
        - $ref: "#/components/parameters/CheckUidPath"
```

whereas `listChecks` declares `with` properly at
[`openapi.yaml:945-953`](../../server/internal/app/openapi/openapi.yaml), with prose
covering both components.

Two consequences:

- The generated docs API reference (`web/docs`, built from `openapi.yaml`) and the
  interactive `/openapi` explorer both show the detail endpoint as taking no query
  parameters. A user reading the reference has no way to discover that
  `GET /api/v1/orgs/:org/checks/:checkUid?with=last_result,last_status_change`
  works at all.
- The generated Go client cannot send it. `GetCheck` is generated without a params
  struct ([`client_generated.go:8274`](../../server/pkg/client/client_generated.go)):

  ```go
  func (c *Client) GetCheck(ctx context.Context, org OrgPath, checkUid CheckUidPath, reqEditors ...RequestEditorFn) (*http.Response, error)
  ```

  Contrast `GetCheckAvailability` at
  [`client_generated.go:8395`](../../server/pkg/client/client_generated.go), which does
  take `params *GetCheckAvailabilityParams`.

This is pre-existing and unrelated to any recent change — it was found while auditing
[`specs/done/2026/08/2026-08-09-07-checks-list-last-result-and-status-change-full-scans.md`](../done/2026/08/2026-08-09-07-checks-list-last-result-and-status-change-full-scans.md),
which rewrote what `last_status_change` *means* but explicitly scoped this gap out.

### The part that isn't a one-line YAML edit

Adding the parameter changes the generated client's **public signature** — oapi-codegen
will emit a `GetCheckParams` struct and thread it through both `GetCheck` and
`GetCheckWithResponse`. Six in-repo call sites break at compile time:

| File | Line |
|---|---|
| [`server/pkg/cli/checks_detail.go`](../../server/pkg/cli/checks_detail.go) | 35 |
| [`server/pkg/cli/checks_deps.go`](../../server/pkg/cli/checks_deps.go) | 82, 445 |
| [`server/test/integration/checks_test.go`](../../server/test/integration/checks_test.go) | 80, 309 |

(`GetCheckJobWithResponse` callers are a different operation and are unaffected.)

## Proposal

1. **Declare the parameter.** Add `with` to the `getCheck` operation in
   [`openapi.yaml`](../../server/internal/app/openapi/openapi.yaml), modelled on the
   `listChecks` block at 945-953 but phrased for a single check ("the check's newest
   raw result" rather than list-oriented wording). Enumerate exactly the two
   components the handler implements — `last_result` and `last_status_change` —
   and keep the `last_status_change` description consistent with what spec
   2026-08-09-07 established: served from the check row itself, costs no extra
   query, and **omitted for checks that have never transitioned**.

   Do not invent components the detail handler doesn't parse, and do not copy the
   list's set wholesale on the assumption it matches — re-read both switches first.
   They happen to agree today; the point is to verify rather than assume.

2. **Regenerate the client**: `go generate ./pkg/client/...` from `server/`.

3. **Fix the six call sites.** Pass `nil` for the new params argument at all of
   them. **Decision, so the implementer doesn't have to guess:** this spec is a
   documentation-fidelity fix and must not change any observable CLI output.
   Wiring `solidping checks detail` to actually request `with=last_result,last_status_change`
   and render the embeds is a real product improvement, but it is a *behavior*
   change with its own output-format and test implications — file it separately if
   wanted. Here, `nil` preserves today's requests byte-for-byte.

4. **Rebuild the docs**: `make build-docs`, and confirm the parameter actually
   surfaces in the generated API reference rather than assuming the pipeline picked
   it up.

## Out of scope

- Making the CLI (or anything else) *use* the new parameter — see the decision in
  step 3.
- Auditing the other `with`-bearing operations in `openapi.yaml` (lines 1739, 1908,
  3680, 5666) for the same class of drift. Worth a separate sweep; this spec fixes
  the one confirmed gap.
- Any change to the handler's behavior. The server already does the right thing;
  only its description is wrong.

## Acceptance criteria

- [ ] `getCheck` in `openapi.yaml` declares a `with` query parameter documenting
      exactly `last_result` and `last_status_change`, with wording consistent with
      the `listChecks` declaration and with the current `last_status_change`
      semantics (including the omitted-when-never-transitioned contract).
- [ ] The set of documented components is verified against
      `handler.go:351-353`, not copied from the list endpoint on faith.
- [ ] `go generate ./pkg/client/...` regenerates cleanly and
      `server/pkg/client/client_generated.go` shows `GetCheck` /
      `GetCheckWithResponse` taking a `*GetCheckParams`.
- [ ] All six call sites compile; `make build-backend lint-back test` is green.
- [ ] No CLI output changes — `checks detail` and `checks deps` issue the same HTTP
      requests as before.
- [ ] `make build-docs` succeeds and the parameter is visible in the generated API
      reference for the check-detail endpoint (confirmed by looking, not inferred).
