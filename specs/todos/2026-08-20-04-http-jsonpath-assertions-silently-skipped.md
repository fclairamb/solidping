---
model: opus
effort: high
---

# HTTP checks with only `json_path_assertions` never read the body, so the assertions are silently skipped and the check always reports UP

## Problem

The HTTP checker decides whether to read the response body at all with a gate that
covers the four `body_*` keys and **not** `json_path_assertions`
([checker.go:365-366](server/internal/checkers/checkhttp/checker.go:365)):

```go
bodyDrivesAssertions := cfg.BodyExpect != "" || cfg.BodyReject != "" ||
    cfg.BodyPattern != "" || cfg.BodyPatternReject != ""
```

The JSONPath assertion block later is guarded by both the config key **and** a
non-empty body ([checker.go:544](server/internal/checkers/checkhttp/checker.go:544)):

```go
if cfg.JSONPathAssertions != nil && respBody != "" {
```

Consequence: a check configured with **only** `json_path_assertions` and no `body_*`
key never reads the body, `respBody` stays `""`, and the assertion block is skipped
without any error or log. Such a check reports UP regardless of what the endpoint
returns — the assertions are dead configuration, and the user believes they are being
enforced.

This is a known, deliberately-deferred bug: it was found during the audit of the
`capture_failure_response` spec (now in `specs/done/2026/08/`), and the comment block at
[checker.go:357-361](server/internal/checkers/checkhttp/checker.go:357) documents that
the capture feature was made observationally inert (it reads `bodyBytes` directly,
never `respBody`) precisely so that enabling a diagnostic toggle would not start
evaluating assertions as a side effect. The underlying bug was explicitly left for its
own spec — this one.

### Why this is not a safe silent fix

Any check that has been reporting UP purely because its assertions never ran will start
evaluating them after the fix — and if the assertions fail (misconfigured, stale, or
the endpoint genuinely unhealthy), the check flips to DOWN and **pages people**. The
one-line fix must therefore not ship without a blast-radius estimate and an explicit
rollout decision from the user.

## Proposal

The code fix is one term in the read gate. `JSONPathAssertions` is a `*AssertionNode`
([config.go:99](server/internal/checkers/checkhttp/config.go:99)), so the term is a nil
check (not a `len(...)` — the field is a pointer, not a slice):

```go
bodyDrivesAssertions := cfg.BodyExpect != "" || cfg.BodyReject != "" ||
    cfg.BodyPattern != "" || cfg.BodyPatternReject != "" ||
    cfg.JSONPathAssertions != nil
```

Also update/remove the now-stale deferral comment at
[checker.go:357-361](server/internal/checkers/checkhttp/checker.go:357), and keep the
`capture_failure_response` inertness intact (it must keep reading `bodyBytes`, not
`respBody` — its property is "enabling capture never changes the verdict", which stays
true after this fix).

**Mandatory sequence — do not ship the gate change without steps 1–3:**

1. **Confirm the bug test-first.** Add a test: an HTTP check configured with *only*
   `json_path_assertions` (no `body_*` key) against a response that should fail the
   assertions. Assert it currently reports UP (bug reproduced), then apply the fix and
   flip the assertion to DOWN. Keep companion cases: same config against a response
   that *satisfies* the assertions stays UP after the fix (positive control), and a
   check combining `json_path_assertions` with a `body_*` key behaves identically
   before/after (those already read the body).

2. **Estimate the blast radius.** Write and run a query counting existing HTTP checks
   whose config has `json_path_assertions` set and none of `body_expect` /
   `body_reject` / `body_pattern` / `body_pattern_reject` — per org, with check names.
   Run it against whatever database is reachable (local dev at minimum); provide the
   SQL so the operator can run it against solidping.k8xp.com and production. That
   count is the set of checks that may flip to DOWN on deploy.

3. **Get a rollout decision from the user** (AskUserQuestion) with the blast-radius
   numbers in hand:
   - **Fix outright** — acceptable if the affected count is zero or the user accepts
     the risk of flips;
   - **Fix behind a notice/migration** — warn affected orgs first (e.g. a changelog
     entry, an in-app notice, or a one-time notification listing their affected
     checks), then enable.

   Whichever path is chosen, the release notes / CHANGELOG entry must call out the
   behavior change explicitly: "JSONPath assertions on HTTP checks without body
   matchers were previously never evaluated; they now are."

### Open questions

- If the user picks the notice/migration path, its exact mechanism (in-app notice vs.
  notification vs. changelog-only) is theirs to choose at the decision gate — do not
  build a migration framework speculatively.
- Production blast-radius numbers may not be obtainable from the implementation
  environment; in that case deliver the SQL and the dev-DB result, and surface the gap
  at the decision gate rather than guessing.

## Resolved open questions

- **Rollout decision (Proposal step 3):** Decided by the user on 2026-08-20 at the batch's
  open-questions gate: **fix outright**. The blast-radius query was already run against the
  local dev DB (`solidping` database): **0 affected checks** (HTTP checks with
  `json_path_assertions` and none of the `body_*` matchers). Do NOT build a notice or
  migration mechanism, and do NOT attempt AskUserQuestion mid-implementation — the decision
  gate is already satisfied. Steps 1 (test-first confirmation) and 2 (deliver the SQL) of
  the mandatory sequence still apply in full: include the exact SQL in the final report so
  the operator can run it against solidping.k8xp.com and production before deploying, and
  put the behavior-change callout ("JSONPath assertions on HTTP checks without body
  matchers were previously never evaluated; they now are.") in the commit body so it
  reaches the release notes.
- **Notice/migration mechanism:** moot — the outright path was chosen.
- **Production blast-radius numbers:** confirmed not obtainable from this environment;
  deliver the SQL plus the dev-DB result (0 rows) and note the gap in the final report.
