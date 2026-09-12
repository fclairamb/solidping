---
model: sonnet
effort: high
---

# The super-admin system-parameters write accepts any key and lets Postgres reject it with a 500

## Problem

`PUT /api/v1/system/parameters/:key` takes the key straight off the URL and hands
it to the database with no validation
([handler.go:78-79](server/internal/handlers/system/handler.go:78)):

```go
func (h *Handler) SetParameter(writer http.ResponseWriter, req *http.Request) error {
	key := httpx.Param(req, "key")
	// ... decode body ...
	param, err := h.svc.SetParameter(req.Context(), key, setReq.Value, secret)
```

`parameters.key` has carried a Postgres CHECK constraint since the released
`001_v0_1_0` baseline
([001_v0_1_0.up.sql:23](server/internal/db/postgres/migrations/001_v0_1_0.up.sql:23)):
`check (key ~ '^[a-z0-9_\.]+$')`. The unreleased `021_v0_28_0` migration widens
it to `'^[a-z0-9_.\-]+$'` to allow hyphens (spec 2026-09-11-03,
[021_v0_28_0.up.sql:101](server/internal/db/postgres/migrations/021_v0_28_0.up.sql:101)).
Either way an uppercase letter, a space, a slash, a colon or a quote violates it,
so the constraint violation surfaces as a raw 500:

```
PUT /api/v1/system/parameters/Uppercase   → 500 INTERNAL_ERROR
  detail: new row for relation "parameters" violates check constraint
          "parameters_key_check" (SQLSTATE=23514)
```

The org-scoped route already does this correctly — `orgparams.Service` calls
`paramkeys.Validate(key)` on Get, Set and Delete
([service.go:120,144,180](server/internal/handlers/orgparams/service.go:120)) and
returns 400 / `VALIDATION_ERROR` / field `key`. The system route has no
equivalent.

### It is also an engine-divergence bug

SQLite's `parameters` table has **no** CHECK on `key`
([001_v0_1_0.up.sql:16-25](server/internal/db/sqlite/migrations/001_v0_1_0.up.sql:16)) —
the column is a bare `text not null`. So the same request that 500s on Postgres
returns **200 and stores the row** on SQLite. A developer running the default
SQLite dev database never sees this at all; it only appears in a Postgres
deployment. Whatever fix lands must be in the application layer so both engines
answer identically, and the test must assert the 400 (a SQLite-backed test that
merely asserts "no 500" passes today for the wrong reason).

### GET and DELETE do not 500, but are still unvalidated

Only `PUT` writes a row, so only `PUT` trips the constraint. `GET` and `DELETE`
issue a SELECT / soft-delete UPDATE that simply matches nothing and answers 404.
That is an acceptable answer, but it is inconsistent with the org route, which
400s on all three.

## Proposal

### 1. Validate the key in `handlers/system` before touching the DB

Reuse `paramkeys`' regex rather than writing a second one — but use
**`paramkeys.KeyPattern`, not `paramkeys.Validate`**.

`Validate` is the *org-admin* gate: on top of the shape check it refuses the
`sp.` reserved prefix
([paramkeys.go:84-97](server/internal/paramkeys/paramkeys.go:84)). That
reservation exists to hold `sp.` **for the platform**, so applying it to the
super-admin route would refuse the platform the very namespace reserved for it.
The system route wants the shape check only.

Verified: **`KeyPattern` (`^[a-z][a-z0-9_.-]{0,63}$`) accepts every platform key
that exists today.** All 102 `ParameterKey` constants under `server/internal/`
match it, and the longest is 44 characters
(`performance.aggregation_retention_day_months`) — comfortably inside the 64-char
cap. The keys written ad hoc rather than via a constant (`encryption.dek`,
`regions`, `custom_regions`, `default_regions`, `telegram.webhook_secret`,
`demo.enabled`, `operator_notifications`, `platform_watchdog`,
`diagnostics.traceroute.enabled`, `status_page.publication_notify_cap`,
`registration.email_pattern`, `registration.slack_workspace_auto_join`) match it
too. So no system-specific pattern is needed — but see the test in §3, which must
*prove* this rather than trust this paragraph.

Apply the check on `SetParameter` and `DeleteParameter` (and `GetParameter`, for
parity with the org route). Return the repo's standard shape via
`h.WriteValidationError` — 400, `VALIDATION_ERROR`, field **`key`**. Note
`handleError`'s existing `ErrInvalidParameter` branch attributes the failure to
field `"value"` ([handler.go:143-146](server/internal/handlers/system/handler.go:143)),
which is wrong for this case; add a `keyField = "key"` constant alongside the
existing `paramValueField` rather than reusing it.

Whether the check lives in the handler or in `system.Service` is the
implementer's call — the service is where `orgparams` puts it, and putting it
there also covers any non-HTTP caller. Either is fine as long as the HTTP answer
is the specified 400.

### 2. Do not touch the org namespace logic

`paramkeys.Validate`'s reserved-prefix behaviour, `OrgKeyPrefix` / `usr.`,
`StorageKey` and `PublicKey` are load-bearing security — they are what stops an
org admin reading `encryption.dek`. This change adds a caller of `KeyPattern`;
it changes nothing in that package's existing behaviour.

Do **not** add a `usr.`-prefix refusal to the system route. System rows carry
`organization_uid IS NULL` and org rows carry a set one, with separate partial
unique indexes
([001_v0_1_0.up.sql:31-34](server/internal/db/postgres/migrations/001_v0_1_0.up.sql:31)),
so a system row named `usr.x` is not reachable through any org's parameters API
and collides with nothing. Refusing it would be unmotivated scope.

### 3. Tests

All of these should run under `make test` (`go test ./... -short`), which is why
they must not need Postgres. `internal/handlers/system` already has an in-memory
SQLite harness (`newOpsEnv`,
[operator_notifications_test.go:23](server/internal/handlers/system/operator_notifications_test.go:23))
— build on it, or test the handler directly with `httptest`.

- **The 400.** `PUT /api/v1/system/parameters/Uppercase` (and a space, a slash, a
  colon, a quote, an empty key, a leading digit) answers 400 with code
  `VALIDATION_ERROR` and field `key`. This test must fail on `main`: today SQLite
  answers 200, so asserting the status code is enough to make it a real
  regression test.
- **Positive control.** A legitimate platform key is still accepted end to end —
  `encryption.dek` at minimum, plus a flat key (`regions`) and a
  three-segment key (`diagnostics.traceroute.enabled`).
- **The registry proof.** A test that iterates the actual `ParameterKey`
  constants (or an explicit list covering the ad-hoc keys named in §1) and
  asserts each one satisfies the chosen pattern. This is what keeps §1's
  "verified" claim true as keys are added, and what would catch a future
  platform key that the pattern refuses.

If any Postgres-backed test is added, confirm it reports `--- PASS` under
`go test ./internal/handlers/system/ -v` (no `-short`), not `--- SKIP`.

## Gate

`make build-backend lint-back test`. Never `make build` or `make ci`.

## Git

This repo squash-merges; branches use the `feat/` / `fix/` / `chore/` prefix —
this one is a `fix/`. The tree is currently on `batch/2026-09-11`: **do not
commit onto it and do not switch this shared tree's branch.** Create the `fix/`
branch from `main` in a separate `git worktree`.

## Implementation Plan

1. `server/internal/handlers/system/service.go`: add `ErrInvalidParameterKey`
   (distinct from the existing `ErrInvalidParameter`, which stays scoped to bad
   *values*) and a small `validateParameterKey(key string) error` helper that
   checks `paramkeys.KeyPattern` (NOT `paramkeys.Validate` — no reserved-prefix
   refusal here, per the spec's §1/§2 rationale). Call it at the top of
   `GetParameter`, `SetParameter` and `DeleteParameter`, before any DB access.
2. `server/internal/handlers/system/handler.go`: add a `keyField = "key"`
   constant alongside `paramValueField`, and a new `errors.Is(err,
   ErrInvalidParameterKey)` branch in `handleError` that calls
   `h.WriteValidationError` with field `keyField` — kept separate from the
   existing `ErrInvalidParameter` branch (field `paramValueField`), since the
   two errors must map to different response fields.
3. Tests in `server/internal/handlers/system/` (new file
   `parameters_key_validation_test.go`), built on the existing `newOpsEnv`
   SQLite harness, driven through `httptest` against the chi router (or
   directly against `Handler.SetParameter`/`GetParameter`/`DeleteParameter`):
   - The 400: PUT (and GET/DELETE) with `Uppercase`, a key containing a space,
     a slash, a colon, a quote, an empty key, and a leading-digit key, each
     answers 400 / `VALIDATION_ERROR` / field `key`.
   - Positive control: `encryption.dek`, `regions`, and
     `diagnostics.traceroute.enabled` are still accepted end to end (200/201,
     row persisted).
   - Registry proof: iterate the actual exported `ParameterKey`-shaped
     constants across the codebase (grep-discovered list) plus the ad-hoc keys
     named in the spec's §1, asserting each matches `paramkeys.KeyPattern`.
4. `make fmt`, then iterate `go build ./internal/handlers/system/...`,
   `golangci-lint run ./internal/handlers/system/...`, and
   `go test ./internal/handlers/system/ -run TestSystemParameter...` until
   green, then run the full gate once from repo root:
   `make build-backend lint-back test`.
