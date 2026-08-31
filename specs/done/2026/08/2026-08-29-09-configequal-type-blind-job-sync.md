# configEqual is type-blind — a config edit that only changes value types never reaches check_jobs

## Problem

`configEqual` in `server/internal/handlers/checks/service.go` decides whether
`reconcileCheckJobs` must copy the check's config onto its `check_jobs` rows. It
compares each value with `fmt.Sprintf("%v", ...)`:

```go
if fmt.Sprintf("%v", valA) != fmt.Sprintf("%v", valB) {
    return false
}
```

`%v` renders `[]interface{}{float64(200), float64(403)}` and
`[]interface{}{"200", "403"}` as the same string, `[200 403]`. So a PATCH that
only changes value *types* — exactly the fix for a config stored with the wrong
types — is judged "equal", `needsUpdate` stays false, and the job keeps
dispatching the broken snapshot forever. The check's own `config` column is
correct; only the denormalized `check_jobs.config` stays stale, which makes the
bug maddening to diagnose from the API (the check looks fixed, the results keep
failing).

## Observed in production (org `public`, 2026-08-29)

1. Checks were created, then patched with `expectedStatusCodes: [200, 403]`
   (JSON numbers). The HTTP checker requires strings and every run failed with
   `failed to parse config: expectedStatusCodes: element 0 must be a string`.
2. A second PATCH set `expectedStatusCodes: ["200", "403"]` (strings). The
   checks' stored config updated, but the `check_jobs.config` rows kept the
   numeric arrays and the parse error persisted indefinitely.
3. Workaround that worked: disable/enable the check (deletes and recreates the
   jobs, which copies the current config unconditionally).

Other `%v` collisions exist beyond numbers-vs-strings (e.g. `"true"` vs `true`,
nested maps whose Go map iteration hides nothing here but whose stringified
forms can coincide), so this is a class, not a one-off.

## Proposal

- Replace the `fmt.Sprintf("%v", ...)` comparison with a type-aware deep
  comparison of the two `models.JSONMap` values. Marshaling both maps to
  canonical JSON (`encoding/json` on the map values, byte-compare) is enough:
  both sides live as decoded-JSON values (`float64`/`string`/`bool`/
  `map[string]interface{}`/`[]interface{}`), so canonical JSON equality is
  exactly "would the worker see a different config".
- Table-driven tests pinning the regression: numeric vs string status-code
  arrays must compare unequal; a genuinely identical config must compare equal
  (so reconcile stays cheap on no-op edits).
- Consider whether `reconcileCheckJobs` even needs the guard for the config
  column, or whether config should be copied unconditionally whenever the
  check row was updated — the guard exists to avoid rescheduling
  (`scheduled_at` is reset when `needsUpdate` fires); splitting "sync config"
  from "reset schedule" would remove the whole failure class: config always
  synced, schedule only reset when period/regions/type change.

## Related

While diagnosing this, the HTTP checker's `expectedStatusCodes` rejecting JSON
numbers was the original trigger. A separate quality-of-life fix worth
considering: accept numbers in `expectedStatusCodes` (coerce to their decimal
string form) — the dashboard/API accept and store them silently today, and the
failure only surfaces at execution time in the worker.
